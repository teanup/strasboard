// Package electricity fetches consumption history from the SER API.
package electricity

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"strasboard/api/config"
	"strasboard/api/httpx"
)

// apiURL is the SER portal endpoint.
const apiURL = "https://maconsolinky.strasbourg-electricite-reseaux.fr/efluidES_PORTAIL"

// refreshHour is the hour at which the previous day's data becomes available.
const refreshHour = 1

// retryInterval is how long to wait before retrying when data is missing.
const retryInterval = 1 * time.Hour

// currentID is the fixed item ID for this single-document resource.
const currentID = "current"

// Source fetches electricity consumption from the SER API.
type Source struct {
	clientID string
	username string
	password string
	loc      *time.Location

	mu             sync.Mutex
	accessToken    string
	tokenExpiry    time.Time
	servicePointID string
	lastData       *Data
}

// Data is the electricity payload returned by the API.
type Data struct {
	Days   []Consumption `json:"days"`
	Months []Consumption `json:"months"`
}

type Consumption struct {
	Date string `json:"date"`
	HC   *int   `json:"HC,omitempty"`
	HP   *int   `json:"HP,omitempty"`
	BCHC *int   `json:"BCHC,omitempty"`
	BCHP *int   `json:"BCHP,omitempty"`
	BUHC *int   `json:"BUHC,omitempty"`
	BUHP *int   `json:"BUHP,omitempty"`
	RHC  *int   `json:"RHC,omitempty"`
	RHP  *int   `json:"RHP,omitempty"`
}

func New(cfg *config.Config) *Source {
	loc, _ := time.LoadLocation("Europe/Paris")
	if loc == nil {
		loc = time.Local
	}
	return &Source{
		clientID: cfg.Electricity.ClientID,
		username: cfg.Electricity.Username,
		password: cfg.Electricity.Password,
		loc:      loc,
	}
}

func (s *Source) Name() string { return "electricity" }

// NextRefresh returns 1 AM once the previous day's data is available, or the
// retry interval while it is still missing.
func (s *Source) NextRefresh(now time.Time) time.Time {
	now = now.In(s.loc)

	s.mu.Lock()
	hasYesterday := s.hasYesterday(now)
	s.mu.Unlock()

	if hasYesterday {
		return time.Date(now.Year(), now.Month(), now.Day()+1, refreshHour, 0, 0, 0, s.loc)
	}
	return now.Add(retryInterval)
}

// hasYesterday reports whether the stored data already includes yesterday.
func (s *Source) hasYesterday(now time.Time) bool {
	if s.lastData == nil || len(s.lastData.Days) == 0 {
		return false
	}
	yesterday := now.AddDate(0, 0, -1).Format(time.DateOnly)
	return s.lastData.Days[len(s.lastData.Days)-1].Date >= yesterday
}

// Refresh fetches consumption data and returns it as a single item.
func (s *Source) Refresh(_ context.Context) (map[string]any, error) {
	if s.username == "" || s.password == "" {
		return nil, fmt.Errorf("electricity not configured")
	}

	data, err := s.fetchData()
	if err != nil {
		return nil, err
	}

	s.mu.Lock()
	s.lastData = data
	s.mu.Unlock()

	return map[string]any{currentID: data}, nil
}

// fetchData fetches consumption once authenticated.
func (s *Source) fetchData() (*Data, error) {
	if err := s.ensureAuth(); err != nil {
		return nil, fmt.Errorf("auth: %w", err)
	}
	return s.fetchConsumption()
}

// ensureAuth ensures a valid access token and service point ID.
func (s *Source) ensureAuth() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.accessToken != "" && time.Now().Add(5*time.Minute).Before(s.tokenExpiry) {
		return nil
	}

	log.Printf("[electricity] authenticating")
	verifier, challenge := generatePKCE()

	cookie, err := s.login()
	if err != nil {
		return fmt.Errorf("login: %w", err)
	}

	code, err := s.authorize(cookie, challenge)
	if err != nil {
		return fmt.Errorf("authorize: %w", err)
	}

	if err := s.exchangeToken(code, verifier); err != nil {
		return fmt.Errorf("token: %w", err)
	}

	log.Printf("[electricity] authenticated (expires in %s)", time.Until(s.tokenExpiry).Round(time.Minute))

	if s.servicePointID == "" {
		if err := s.fetchServicePoint(); err != nil {
			return fmt.Errorf("service point: %w", err)
		}
	}
	return nil
}

// login logs in and returns the session cookie.
func (s *Source) login() (*http.Cookie, error) {
	var resp struct {
		Code    string `json:"code"`
		Libelle string `json:"libelle"`
	}

	httpResp, err := httpx.PostForm(apiURL+"/auth/externe/authentification", url.Values{
		"username":  {s.username},
		"password":  {s.password},
		"client_id": {s.clientID},
	}, nil, nil, &resp, nil)
	if err != nil {
		return nil, err
	}

	if resp.Code != "0" {
		return nil, fmt.Errorf("%s", resp.Libelle)
	}

	for _, c := range httpResp.Cookies() {
		if c.Name == "cookieOauth" {
			return c, nil
		}
	}
	return nil, fmt.Errorf("no session cookie")
}

// authorize obtains an authorization code.
func (s *Source) authorize(cookie *http.Cookie, challenge string) (string, error) {
	query := url.Values{
		"response_type":         {"code"},
		"code_challenge":        {challenge},
		"code_challenge_method": {"S256"},
		"client_id":             {s.clientID},
	}

	httpResp, err := httpx.GetRedirect(apiURL+"/auth/authorize-internet", query, nil, []*http.Cookie{cookie})
	if err != nil {
		return "", err
	}

	loc := httpResp.Header.Get("Location")
	if loc == "" {
		return "", fmt.Errorf("no redirect")
	}

	parsedLoc, err := url.Parse(loc)
	if err != nil {
		return "", fmt.Errorf("parse redirect: %w", err)
	}

	code := parsedLoc.Query().Get("code")
	if code == "" {
		return "", fmt.Errorf("no authorization code")
	}
	return code, nil
}

// exchangeToken exchanges an authorization code for an access token.
func (s *Source) exchangeToken(code, verifier string) error {
	var resp struct {
		AccessToken string `json:"access_token"`
		TokenType   string `json:"token_type"`
		ExpiresIn   int    `json:"expires_in"`
		Error       string `json:"error"`
	}

	if _, err := httpx.PostForm(apiURL+"/auth/tokenUtilisateurInternet", url.Values{
		"client_id":     {s.clientID},
		"code":          {code},
		"grant_type":    {"authorization_code"},
		"code_verifier": {verifier},
	}, nil, nil, &resp, nil); err != nil {
		return err
	}

	if resp.Error != "" {
		return fmt.Errorf("%s", resp.Error)
	}

	s.accessToken = resp.TokenType + " " + resp.AccessToken
	s.tokenExpiry = time.Now().Add(time.Duration(resp.ExpiresIn) * time.Second)
	return nil
}

// fetchServicePoint fetches the service point ID (Point De Livraison).
func (s *Source) fetchServicePoint() error {
	var resp []struct {
		ID             string `json:"id"`
		PointDeService struct {
			Reference string `json:"reference"`
		} `json:"pointDeService"`
	}

	query := url.Values{"expand": {"pointDeService"}}
	headers := http.Header{"Authorization": {s.accessToken}}
	if _, err := httpx.GetJSON(apiURL+"/rest/produits/pointsAccesServicesClient", query, headers, nil, &resp, nil); err != nil {
		return err
	}

	if len(resp) == 0 {
		return fmt.Errorf("no service point found")
	}

	s.servicePointID = resp[0].ID
	log.Printf("[electricity] service point %s", resp[0].PointDeService.Reference)
	return nil
}

// consumptionPeriod is a consumption period from the API response.
type consumptionPeriod struct {
	BlocFournisseur struct {
		PostesHorosaisonnier []struct {
			Etiquette struct {
				Mnemo string `json:"mnemo"`
			} `json:"etiquette"`
			ConsommationsJournalieres []struct {
				Date         string   `json:"date"`
				Consommation *float64 `json:"consommation"`
			} `json:"consommationsJournalieres"`
			ConsommationsMensuelles []struct {
				Annee        int     `json:"annee"`
				Mois         int     `json:"mois"`
				Consommation float64 `json:"consommation"`
			} `json:"consommationsMensuelles"`
		} `json:"postesHorosaisonnier"`
	} `json:"blocFournisseur"`
}

// fetchConsumption fetches electricity consumption data.
func (s *Source) fetchConsumption() (*Data, error) {
	now := time.Now()
	start := time.Date(now.Year(), now.Month()-2, 0, 0, 0, 0, 0, time.Local)
	end := time.Date(now.Year(), now.Month(), now.Day()+1, 0, 0, 0, 0, time.Local)

	payload := map[string]any{
		"typeObjet": "DonneesHistoriqueMesureRepresentation",
		"dateDebut": start.Format(time.RFC3339),
		"dateFin":   end.Format(time.RFC3339),
		"pointAccesServicesClient": map[string]any{
			"typeObjet": "produit.PointAccesServicesClient",
			"id":        s.servicePointID,
		},
		"groupesDeGrandeurs": []map[string]any{
			{"typeObjet": "produit.GroupeGrandeur", "codeGroupeGrandeur": map[string]string{"code": "3"}},
		},
	}

	var resp struct {
		PeriodesActivite []consumptionPeriod `json:"periodesActivite"`
	}

	reqURL := apiURL + "/rest/interfaces/" + strings.ToLower(s.clientID) + "/historiqueDeMesure"
	headers := http.Header{"Authorization": {s.accessToken}}
	if _, err := httpx.PostJSON(reqURL, payload, headers, nil, &resp, checkErrSER); err != nil {
		return nil, err
	}

	if len(resp.PeriodesActivite) == 0 {
		return nil, fmt.Errorf("no contract data")
	}

	return s.parseConsumption(resp.PeriodesActivite), nil
}

// parseConsumption parses consumption data from the API response.
func (s *Source) parseConsumption(contracts []consumptionPeriod) *Data {
	daily := make(map[string]map[string]float64)
	monthly := make(map[string]map[string]float64)

	for _, contract := range contracts {
		for _, poste := range contract.BlocFournisseur.PostesHorosaisonnier {
			tariff := poste.Etiquette.Mnemo
			if tariff == "" {
				continue
			}

			for _, c := range poste.ConsommationsJournalieres {
				if c.Consommation == nil {
					continue
				}
				parts := strings.Split(c.Date, "/")
				if len(parts) != 3 {
					continue
				}
				date := parts[2] + "-" + parts[1] + "-" + parts[0]
				if daily[date] == nil {
					daily[date] = make(map[string]float64)
				}
				daily[date][tariff] = *c.Consommation
			}

			for _, c := range poste.ConsommationsMensuelles {
				month := fmt.Sprintf("%d-%02d", c.Annee, c.Mois)
				if monthly[month] == nil {
					monthly[month] = make(map[string]float64)
				}
				monthly[month][tariff] = c.Consommation
			}
		}
	}

	return &Data{
		Days:   aggregateConsumption(daily, 14),
		Months: aggregateConsumption(monthly, 2),
	}
}

// generatePKCE generates a PKCE verifier and challenge.
func generatePKCE() (verifier, challenge string) {
	b := make([]byte, 32)
	rand.Read(b)
	verifier = base64.RawURLEncoding.EncodeToString(b)
	hash := sha256.Sum256([]byte(verifier))
	challenge = base64.RawURLEncoding.EncodeToString(hash[:])
	return
}

// aggregateConsumption aggregates consumption data into a sorted slice with a limit.
func aggregateConsumption(m map[string]map[string]float64, limit int) []Consumption {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	if len(keys) > limit {
		keys = keys[len(keys)-limit:]
	}

	result := make([]Consumption, len(keys))
	for i, k := range keys {
		c := Consumption{Date: k}
		for tariff, value := range m[k] {
			v := int(value + 0.5)
			if v == 0 {
				continue
			}
			switch tariff {
			case "HC":
				c.HC = &v
			case "HP":
				c.HP = &v
			case "BCHC":
				c.BCHC = &v
			case "BCHP":
				c.BCHP = &v
			case "BUHC":
				c.BUHC = &v
			case "BUHP":
				c.BUHP = &v
			case "RHC":
				c.RHC = &v
			case "RHP":
				c.RHP = &v
			}
		}
		result[i] = c
	}
	return result
}

// checkErrSER returns an error if the SER response reports one.
func checkErrSER(body []byte) error {
	var resp struct {
		MessagesInformatifs []string `json:"messagesInformatifs"`
	}
	if err := json.Unmarshal(body, &resp); err == nil && len(resp.MessagesInformatifs) > 0 {
		return fmt.Errorf("%s", strings.Join(resp.MessagesInformatifs, "; "))
	}
	return nil
}
