package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Client Microsoft Graph minimal : uniquement les appels OneDrive/SharePoint
// nécessaires (dossiers, fichiers, invitations, permissions).

var grapheBase = "https://graph.microsoft.com/v1.0"

// Au-delà, l'envoi passe par une session de téléversement (morceaux).
const seuilEnvoiSimple = 4 << 20

// Taille d'un morceau : multiple de 320 Kio exigé par Graph.
const tailleMorceau = 32 * 320 << 10

var (
	ErrIntrouvable = errors.New("introuvable sur Microsoft 365")
	ErrConflit     = errors.New("modifié entre-temps par un autre poste")
)

type ErreurGraphe struct {
	Statut  int
	Code    string
	Message string
}

func (e *ErreurGraphe) Error() string {
	return fmt.Sprintf("Microsoft 365 (%d %s) : %s", e.Statut, e.Code, e.Message)
}

type Graphe struct {
	auth   *Auth
	client *http.Client

	mu      sync.Mutex
	driveID string
	pourCfg Config // configuration pour laquelle driveID a été résolu
}

func NouveauGraphe(a *Auth) *Graphe {
	return &Graphe{auth: a, client: &http.Client{Timeout: 10 * time.Minute}}
}

type Element struct {
	ID      string    `json:"id"`
	Nom     string    `json:"name"`
	Taille  int64     `json:"size"`
	ModifLe time.Time `json:"lastModifiedDateTime"`
	WebURL  string    `json:"webUrl"`
	ETag    string    `json:"eTag"`
	Dossier *struct{} `json:"folder,omitempty"`
	Parent  *struct {
		ID string `json:"id"`
	} `json:"parentReference,omitempty"`
	ModifPar *struct {
		User struct {
			DisplayName string `json:"displayName"`
		} `json:"user"`
	} `json:"lastModifiedBy,omitempty"`
}

// appel exécute une requête Graph et décode la réponse JSON dans sortie.
func (g *Graphe) appel(ctx context.Context, methode, chemin string, entete http.Header, corps io.Reader, sortie any) (http.Header, error) {
	jeton, err := g.auth.Jeton(ctx)
	if err != nil {
		return nil, err
	}
	u := chemin
	if strings.HasPrefix(chemin, "/") {
		u = grapheBase + chemin
	}
	req, err := http.NewRequestWithContext(ctx, methode, u, corps)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+jeton)
	for k, v := range entete {
		req.Header[k] = v
	}
	resp, err := g.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("Microsoft 365 injoignable (connexion Internet ?) : %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return resp.Header, erreurReponse(resp)
	}
	if sortie != nil && resp.StatusCode != http.StatusNoContent {
		if b, ok := sortie.(*[]byte); ok {
			*b, err = io.ReadAll(resp.Body)
			return resp.Header, err
		}
		if err := json.NewDecoder(resp.Body).Decode(sortie); err != nil {
			return resp.Header, fmt.Errorf("réponse Microsoft 365 illisible : %w", err)
		}
	}
	return resp.Header, nil
}

func erreurReponse(resp *http.Response) error {
	var e struct {
		Error struct{ Code, Message string } `json:"error"`
	}
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	_ = json.Unmarshal(b, &e)
	switch resp.StatusCode {
	case http.StatusNotFound:
		return ErrIntrouvable
	case http.StatusPreconditionFailed, http.StatusConflict:
		if e.Error.Code != "nameAlreadyExists" {
			return ErrConflit
		}
	case http.StatusUnauthorized:
		return ErrNonConnecte
	}
	return &ErreurGraphe{Statut: resp.StatusCode, Code: e.Error.Code, Message: e.Error.Message}
}

func jsonCorps(v any) (http.Header, io.Reader) {
	b, _ := json.Marshal(v)
	return http.Header{"Content-Type": {"application/json"}}, bytes.NewReader(b)
}

// chemin encode chaque segment d'un chemin relatif à la racine du lecteur.
func cheminEncode(segments ...string) string {
	var parts []string
	for _, s := range segments {
		for _, p := range strings.Split(s, "/") {
			if p != "" {
				parts = append(parts, url.PathEscape(p))
			}
		}
	}
	return strings.Join(parts, "/")
}

// Lecteur renvoie l'identifiant de la bibliothèque de documents utilisée :
// celle du site SharePoint configuré, sinon le OneDrive de l'utilisateur.
func (g *Graphe) Lecteur(ctx context.Context) (string, error) {
	cfg := g.auth.Config()
	g.mu.Lock()
	if g.driveID != "" && g.pourCfg == cfg {
		id := g.driveID
		g.mu.Unlock()
		return id, nil
	}
	g.mu.Unlock()

	var d struct{ ID string }
	if cfg.SiteURL == "" {
		if _, err := g.appel(ctx, "GET", "/me/drive?$select=id", nil, nil, &d); err != nil {
			return "", err
		}
	} else {
		u, err := url.Parse(cfg.SiteURL)
		if err != nil || u.Host == "" {
			return "", errors.New("adresse du site SharePoint invalide")
		}
		var s struct{ ID string }
		chemin := "/sites/" + u.Host
		if p := strings.Trim(u.Path, "/"); p != "" {
			chemin += ":/" + cheminEncode(p)
		}
		if _, err := g.appel(ctx, "GET", chemin+"?$select=id", nil, nil, &s); err != nil {
			if errors.Is(err, ErrIntrouvable) {
				return "", errors.New("site SharePoint introuvable : vérifier l'adresse dans les réglages")
			}
			return "", err
		}
		if _, err := g.appel(ctx, "GET", "/sites/"+s.ID+"/drive?$select=id", nil, nil, &d); err != nil {
			return "", err
		}
	}
	g.mu.Lock()
	g.driveID, g.pourCfg = d.ID, cfg
	g.mu.Unlock()
	return d.ID, nil
}

// ElementParChemin lit un élément par son chemin depuis la racine du lecteur.
func (g *Graphe) ElementParChemin(ctx context.Context, chemin string) (*Element, error) {
	d, err := g.Lecteur(ctx)
	if err != nil {
		return nil, err
	}
	var e Element
	_, err = g.appel(ctx, "GET", "/drives/"+d+"/root:/"+cheminEncode(chemin), nil, nil, &e)
	return &e, err
}

// AssurerDossier crée si besoin le dossier au chemin donné (un seul niveau
// sous la racine du lecteur) et le renvoie.
func (g *Graphe) AssurerDossier(ctx context.Context, chemin string) (*Element, error) {
	e, err := g.ElementParChemin(ctx, chemin)
	if err == nil || !errors.Is(err, ErrIntrouvable) {
		return e, err
	}
	d, _ := g.Lecteur(ctx)
	h, corps := jsonCorps(map[string]any{"name": chemin, "folder": map[string]any{}, "@microsoft.graph.conflictBehavior": "fail"})
	var n Element
	_, err = g.appel(ctx, "POST", "/drives/"+d+"/root/children", h, corps, &n)
	return &n, err
}

// CreerDossier crée un sous-dossier ; renomme automatiquement en cas de doublon.
func (g *Graphe) CreerDossier(ctx context.Context, parentID, nom string) (*Element, error) {
	d, err := g.Lecteur(ctx)
	if err != nil {
		return nil, err
	}
	h, corps := jsonCorps(map[string]any{"name": nom, "folder": map[string]any{}, "@microsoft.graph.conflictBehavior": "rename"})
	var n Element
	_, err = g.appel(ctx, "POST", "/drives/"+d+"/items/"+parentID+"/children", h, corps, &n)
	return &n, err
}

func (g *Graphe) Element(ctx context.Context, id string) (*Element, error) {
	d, err := g.Lecteur(ctx)
	if err != nil {
		return nil, err
	}
	var e Element
	_, err = g.appel(ctx, "GET", "/drives/"+d+"/items/"+id, nil, nil, &e)
	return &e, err
}

func (g *Graphe) Renommer(ctx context.Context, id, nom string) error {
	d, err := g.Lecteur(ctx)
	if err != nil {
		return err
	}
	h, corps := jsonCorps(map[string]any{"name": nom})
	_, err = g.appel(ctx, "PATCH", "/drives/"+d+"/items/"+id, h, corps, nil)
	return err
}

func (g *Graphe) Enfants(ctx context.Context, id string) ([]Element, error) {
	d, err := g.Lecteur(ctx)
	if err != nil {
		return nil, err
	}
	var tous []Element
	suivant := "/drives/" + d + "/items/" + id + "/children?$top=200&$select=id,name,size,lastModifiedDateTime,webUrl,folder,file,lastModifiedBy"
	for suivant != "" {
		var r struct {
			Value []Element `json:"value"`
			Next  string    `json:"@odata.nextLink"`
		}
		if _, err := g.appel(ctx, "GET", suivant, nil, nil, &r); err != nil {
			return nil, err
		}
		tous = append(tous, r.Value...)
		suivant = r.Next
	}
	return tous, nil
}

func (g *Graphe) Supprimer(ctx context.Context, id string) error {
	d, err := g.Lecteur(ctx)
	if err != nil {
		return err
	}
	_, err = g.appel(ctx, "DELETE", "/drives/"+d+"/items/"+id, nil, nil, nil)
	if errors.Is(err, ErrIntrouvable) {
		return nil
	}
	return err
}

// LireFichier renvoie le contenu et l'eTag d'un fichier désigné par chemin.
func (g *Graphe) LireFichier(ctx context.Context, chemin string) ([]byte, string, error) {
	e, err := g.ElementParChemin(ctx, chemin)
	if err != nil {
		return nil, "", err
	}
	d, _ := g.Lecteur(ctx)
	var b []byte
	_, err = g.appel(ctx, "GET", "/drives/"+d+"/items/"+e.ID+"/content", nil, nil, &b)
	return b, e.ETag, err
}

// EcrireFichier remplace un petit fichier désigné par chemin, seulement s'il
// n'a pas changé depuis la lecture (eTag) ; etag vide = création exclusive.
func (g *Graphe) EcrireFichier(ctx context.Context, chemin string, contenu []byte, etag string) (string, error) {
	d, err := g.Lecteur(ctx)
	if err != nil {
		return "", err
	}
	h := http.Header{"Content-Type": {"application/json"}}
	if etag == "" {
		h.Set("If-None-Match", "*")
	} else {
		h.Set("If-Match", etag)
	}
	var e Element
	_, err = g.appel(ctx, "PUT", "/drives/"+d+"/root:/"+cheminEncode(chemin)+":/content", h, bytes.NewReader(contenu), &e)
	return e.ETag, err
}

// Envoyer téléverse un fichier dans un dossier (renommé si le nom existe).
func (g *Graphe) Envoyer(ctx context.Context, parentID, nom string, taille int64, contenu io.Reader) (*Element, error) {
	d, err := g.Lecteur(ctx)
	if err != nil {
		return nil, err
	}
	base := "/drives/" + d + "/items/" + parentID + ":/" + url.PathEscape(nom) + ":"
	if taille <= seuilEnvoiSimple {
		var e Element
		_, err = g.appel(ctx, "PUT", base+"/content?@microsoft.graph.conflictBehavior=rename",
			http.Header{"Content-Type": {"application/octet-stream"}}, contenu, &e)
		return &e, err
	}

	h, corps := jsonCorps(map[string]any{"item": map[string]any{"@microsoft.graph.conflictBehavior": "rename"}})
	var session struct {
		UploadURL string `json:"uploadUrl"`
	}
	if _, err := g.appel(ctx, "POST", base+"/createUploadSession", h, corps, &session); err != nil {
		return nil, err
	}
	tampon := make([]byte, tailleMorceau)
	var debut int64
	for debut < taille {
		n, err := io.ReadFull(contenu, tampon)
		if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
			return nil, err
		}
		if n == 0 {
			return nil, errors.New("envoi interrompu : fichier incomplet")
		}
		// L'adresse de session est pré-autorisée : pas d'en-tête Authorization.
		req, _ := http.NewRequestWithContext(ctx, "PUT", session.UploadURL, bytes.NewReader(tampon[:n]))
		req.ContentLength = int64(n)
		req.Header.Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", debut, debut+int64(n)-1, taille))
		resp, err := g.client.Do(req)
		if err != nil {
			return nil, fmt.Errorf("envoi interrompu : %w", err)
		}
		if resp.StatusCode >= 300 {
			err := erreurReponse(resp)
			resp.Body.Close()
			return nil, err
		}
		debut += int64(n)
		if debut >= taille {
			var e Element
			err := json.NewDecoder(resp.Body).Decode(&e)
			resp.Body.Close()
			return &e, err
		}
		resp.Body.Close()
	}
	return nil, errors.New("envoi vide")
}

// Inviter partage un élément avec une adresse e-mail. Microsoft envoie
// l'invitation ; une personne extérieure sans compte reçoit un code par
// e-mail à chaque ouverture. Renvoie l'identifiant de la permission créée.
func (g *Graphe) Inviter(ctx context.Context, id, email string, ecriture, envoyerMail bool, message string) (string, error) {
	d, err := g.Lecteur(ctx)
	if err != nil {
		return "", err
	}
	role := "read"
	if ecriture {
		role = "write"
	}
	corps := map[string]any{
		"recipients":     []map[string]string{{"email": email}},
		"requireSignIn":  true,
		"sendInvitation": envoyerMail,
		"roles":          []string{role},
	}
	if message != "" {
		corps["message"] = message
	}
	h, r := jsonCorps(corps)
	var res struct {
		Value []struct {
			ID string `json:"id"`
		} `json:"value"`
	}
	if _, err := g.appel(ctx, "POST", "/drives/"+d+"/items/"+id+"/invite", h, r, &res); err != nil {
		return "", err
	}
	if len(res.Value) == 0 || res.Value[0].ID == "" {
		return "", errors.New("Microsoft 365 n'a pas confirmé le partage")
	}
	return res.Value[0].ID, nil
}

// RetirerDuLien retire une seule personne d'un lien de partage commun à
// plusieurs invités (SharePoint regroupe parfois les externes sur un lien).
func (g *Graphe) RetirerDuLien(ctx context.Context, id, permID, email string) error {
	d, err := g.Lecteur(ctx)
	if err != nil {
		return err
	}
	h, corps := jsonCorps(map[string]any{"grantees": []map[string]string{{"email": email}}})
	_, err = g.appel(ctx, "POST", "/drives/"+d+"/items/"+id+"/permissions/"+url.PathEscape(permID)+"/revokeGrants", h, corps, nil)
	if errors.Is(err, ErrIntrouvable) {
		return nil
	}
	return err
}

// RetirerPermission supprime un accès ; déjà absent = rien à faire.
func (g *Graphe) RetirerPermission(ctx context.Context, id, permID string) error {
	d, err := g.Lecteur(ctx)
	if err != nil {
		return err
	}
	_, err = g.appel(ctx, "DELETE", "/drives/"+d+"/items/"+id+"/permissions/"+url.PathEscape(permID), nil, nil, nil)
	if errors.Is(err, ErrIntrouvable) {
		return nil
	}
	return err
}
