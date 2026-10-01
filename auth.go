package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Connexion Microsoft 365 : flux « code d'autorisation + PKCE » d'une
// application cliente publique (pas de secret). Le jeton d'actualisation est
// conservé sur le poste, chiffré par Windows (DPAPI) pour l'utilisateur courant.

const scopes = "offline_access User.Read Files.ReadWrite.All Sites.ReadWrite.All"

var ErrNonConnecte = errors.New("non connecté à Microsoft 365")

type Config struct {
	TenantID      string `json:"tenantId"`
	ClientID      string `json:"clientId"`
	SiteURL       string `json:"siteUrl"`       // vide = OneDrive de la personne connectée
	DossierRacine string `json:"dossierRacine"` // dossier contenant tous les projets
	NomSalle      string `json:"nomSalle"`      // signature des invitations
}

func (c Config) Complete() bool {
	return c.TenantID != "" && c.ClientID != "" && c.DossierRacine != ""
}

type Compte struct {
	Nom   string `json:"nom"`
	Email string `json:"email"`
}

type Auth struct {
	dir      string
	redirect string
	client   *http.Client

	mu        sync.Mutex
	cfg       Config
	refresh   string
	acces     string
	expire    time.Time
	compte    *Compte
	enAttente map[string]string // state → code_verifier
}

func NouvelleAuth(dir, redirect string) (*Auth, error) {
	a := &Auth{dir: dir, redirect: redirect, client: &http.Client{Timeout: 60 * time.Second}, enAttente: map[string]string{}}
	a.cfg = Config{DossierRacine: "Régie Partage"}
	if b, err := os.ReadFile(filepath.Join(dir, "config.json")); err == nil {
		if err := json.Unmarshal(b, &a.cfg); err != nil {
			return nil, fmt.Errorf("config.json illisible : %w", err)
		}
	}
	if b, err := os.ReadFile(filepath.Join(dir, "jeton.bin")); err == nil {
		if clair, err := deproteger(b); err == nil {
			var j struct {
				Refresh string  `json:"refresh"`
				Compte  *Compte `json:"compte"`
			}
			if json.Unmarshal(clair, &j) == nil {
				a.refresh, a.compte = j.Refresh, j.Compte
			}
		}
	}
	return a, nil
}

func (a *Auth) Config() Config {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.cfg
}

// EnregistrerConfig remplace la configuration ; changer de tenant ou
// d'application oblige à se reconnecter.
func (a *Auth) EnregistrerConfig(c Config) error {
	c.TenantID = strings.TrimSpace(c.TenantID)
	c.ClientID = strings.TrimSpace(c.ClientID)
	c.SiteURL = strings.TrimRight(strings.TrimSpace(c.SiteURL), "/")
	c.DossierRacine = strings.Trim(strings.TrimSpace(c.DossierRacine), "/")
	c.NomSalle = strings.TrimSpace(c.NomSalle)
	if c.DossierRacine == "" {
		c.DossierRacine = "Régie Partage"
	}
	if strings.ContainsAny(c.DossierRacine, `/\:*?"<>|#%`) {
		return errors.New("nom de dossier racine invalide")
	}
	b, _ := json.MarshalIndent(c, "", "  ")
	if err := os.WriteFile(filepath.Join(a.dir, "config.json"), b, 0o600); err != nil {
		return err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if c.TenantID != a.cfg.TenantID || c.ClientID != a.cfg.ClientID {
		a.oublierLocked()
	}
	a.cfg = c
	return nil
}

func (a *Auth) Compte() *Compte {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.refresh == "" {
		return nil
	}
	return a.compte
}

func (a *Auth) point(p string) string {
	return "https://login.microsoftonline.com/" + url.PathEscape(a.cfg.TenantID) + "/oauth2/v2.0/" + p
}

// URLConnexion prépare une connexion et renvoie la page Microsoft à ouvrir.
func (a *Auth) URLConnexion() (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.cfg.Complete() {
		return "", errors.New("renseigner d'abord les réglages Microsoft 365")
	}
	state, verifier := aleatoire(16), aleatoire(32)
	if len(a.enAttente) > 20 {
		a.enAttente = map[string]string{}
	}
	a.enAttente[state] = verifier
	h := sha256.Sum256([]byte(verifier))
	q := url.Values{
		"client_id":             {a.cfg.ClientID},
		"response_type":         {"code"},
		"redirect_uri":          {a.redirect},
		"response_mode":         {"query"},
		"scope":                 {scopes},
		"state":                 {state},
		"code_challenge":        {base64.RawURLEncoding.EncodeToString(h[:])},
		"code_challenge_method": {"S256"},
		"prompt":                {"select_account"},
	}
	return a.point("authorize") + "?" + q.Encode(), nil
}

// Retour termine la connexion avec le code renvoyé par Microsoft.
func (a *Auth) Retour(ctx context.Context, code, state string) error {
	a.mu.Lock()
	verifier, ok := a.enAttente[state]
	delete(a.enAttente, state)
	a.mu.Unlock()
	if !ok {
		return errors.New("demande de connexion inconnue ou expirée, recommencer")
	}
	err := a.demanderJeton(ctx, url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {a.redirect},
		"code_verifier": {verifier},
	})
	if err != nil {
		return err
	}
	return a.chargerCompte(ctx)
}

func (a *Auth) chargerCompte(ctx context.Context) error {
	jeton, err := a.Jeton(ctx)
	if err != nil {
		return err
	}
	req, _ := http.NewRequestWithContext(ctx, "GET", grapheBase+"/me?$select=displayName,mail,userPrincipalName", nil)
	req.Header.Set("Authorization", "Bearer "+jeton)
	resp, err := a.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	var me struct{ DisplayName, Mail, UserPrincipalName string }
	if err := json.NewDecoder(resp.Body).Decode(&me); err != nil {
		return err
	}
	email := me.Mail
	if email == "" {
		email = me.UserPrincipalName
	}
	a.mu.Lock()
	a.compte = &Compte{Nom: me.DisplayName, Email: email}
	err = a.sauverLocked()
	a.mu.Unlock()
	return err
}

// Jeton renvoie un jeton d'accès valide, actualisé si besoin.
func (a *Auth) Jeton(ctx context.Context) (string, error) {
	a.mu.Lock()
	if a.acces != "" && time.Until(a.expire) > 2*time.Minute {
		j := a.acces
		a.mu.Unlock()
		return j, nil
	}
	refresh := a.refresh
	a.mu.Unlock()
	if refresh == "" {
		return "", ErrNonConnecte
	}
	if err := a.demanderJeton(ctx, url.Values{"grant_type": {"refresh_token"}, "refresh_token": {refresh}}); err != nil {
		return "", err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.acces, nil
}

func (a *Auth) demanderJeton(ctx context.Context, v url.Values) error {
	a.mu.Lock()
	cfg := a.cfg
	a.mu.Unlock()
	v.Set("client_id", cfg.ClientID)
	v.Set("scope", scopes)
	req, _ := http.NewRequestWithContext(ctx, "POST", a.point("token"), strings.NewReader(v.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := a.client.Do(req)
	if err != nil {
		return fmt.Errorf("Microsoft 365 injoignable (connexion Internet ?) : %w", err)
	}
	defer resp.Body.Close()
	var r struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int    `json:"expires_in"`
		Error        string `json:"error"`
		Description  string `json:"error_description"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return fmt.Errorf("réponse Microsoft illisible : %w", err)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if r.Error != "" || r.AccessToken == "" {
		if r.Error == "invalid_grant" && v.Get("grant_type") == "refresh_token" {
			a.oublierLocked()
			return ErrNonConnecte
		}
		desc, _, _ := strings.Cut(r.Description, "\r\n")
		return fmt.Errorf("connexion refusée par Microsoft : %s %s", r.Error, desc)
	}
	a.acces = r.AccessToken
	a.expire = time.Now().Add(time.Duration(r.ExpiresIn) * time.Second)
	if r.RefreshToken != "" {
		a.refresh = r.RefreshToken
	}
	return a.sauverLocked()
}

func (a *Auth) Deconnecter() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.oublierLocked()
}

func (a *Auth) oublierLocked() {
	a.refresh, a.acces, a.compte = "", "", nil
	_ = os.Remove(filepath.Join(a.dir, "jeton.bin"))
}

func (a *Auth) sauverLocked() error {
	clair, _ := json.Marshal(map[string]any{"refresh": a.refresh, "compte": a.compte})
	chiffre, err := proteger(clair)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(a.dir, "jeton.bin"), chiffre, 0o600)
}

func aleatoire(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}
