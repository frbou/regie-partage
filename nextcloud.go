package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Fournisseur Nextcloud : fichiers par WebDAV, partages par l'API OCS.
// Connexion par « Login Flow v2 » : la personne autorise Régie Partage dans
// son navigateur, Nextcloud remet un mot de passe d'application révocable.
//
// Chaque régisseur reçoit un partage personnel protégé par mot de passe :
// partage par e-mail (Nextcloud envoie le lien) si l'application « Share by
// mail » est active, sinon lien personnel à transmettre soi-même.

const ncAgent = "Régie Partage"

// Envoi par morceaux au-delà de cette taille (limites des serveurs web).
const ncSeuilMorceaux = 10 << 20

const ncTailleMorceau = 10 << 20

// Droits OCS : 1 = lecture ; 15 = lecture, modification, ajout, suppression.
const (
	ncDroitLecture  = 1
	ncDroitEcriture = 15
)

const (
	ncPartageLien  = 3
	ncPartageEmail = 4
)

type Nextcloud struct {
	auth   *Auth
	client *http.Client

	mu        sync.Mutex
	connexion *connexionEnCours
}

type connexionEnCours struct {
	erreur string
	fini   bool
}

func NouveauNextcloud(a *Auth) *Nextcloud {
	return &Nextcloud{auth: a, client: &http.Client{Timeout: 10 * time.Minute}}
}

func (n *Nextcloud) base() string { return n.auth.Config().NextcloudURL }

// --- Connexion (Login Flow v2) ---

// DemarrerConnexion renvoie la page Nextcloud où autoriser l'application ;
// la fin de la connexion est attendue en arrière-plan.
func (n *Nextcloud) DemarrerConnexion(ctx context.Context) (string, error) {
	if n.base() == "" {
		return "", errors.New("renseigner d'abord l'adresse du serveur Nextcloud")
	}
	req, _ := http.NewRequestWithContext(ctx, "POST", n.base()+"/index.php/login/v2", nil)
	req.Header.Set("User-Agent", ncAgent)
	resp, err := n.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("serveur Nextcloud injoignable : %w", err)
	}
	defer resp.Body.Close()
	var r struct {
		Poll  struct{ Token, Endpoint string }
		Login string
	}
	if resp.StatusCode != 200 || json.NewDecoder(resp.Body).Decode(&r) != nil || r.Login == "" {
		return "", fmt.Errorf("ce serveur ne répond pas comme un Nextcloud (HTTP %d) : vérifier l'adresse", resp.StatusCode)
	}
	en := &connexionEnCours{}
	n.mu.Lock()
	n.connexion = en
	n.mu.Unlock()
	go n.attendreConnexion(en, r.Poll.Endpoint, r.Poll.Token)
	return r.Login, nil
}

func (n *Nextcloud) attendreConnexion(en *connexionEnCours, endpoint, token string) {
	fin := func(msg string) {
		n.mu.Lock()
		en.fini, en.erreur = true, msg
		n.mu.Unlock()
	}
	limite := time.Now().Add(20 * time.Minute)
	for time.Now().Before(limite) {
		time.Sleep(2 * time.Second)
		n.mu.Lock()
		abandon := n.connexion != en
		n.mu.Unlock()
		if abandon {
			return
		}
		resp, err := n.client.PostForm(endpoint, url.Values{"token": {token}})
		if err != nil {
			continue
		}
		if resp.StatusCode != 200 {
			resp.Body.Close()
			continue
		}
		var r struct{ LoginName, AppPassword string }
		err = json.NewDecoder(resp.Body).Decode(&r)
		resp.Body.Close()
		if err != nil || r.AppPassword == "" {
			fin("réponse de connexion Nextcloud illisible")
			return
		}
		id := IdentNextcloud{Login: r.LoginName, MotDePasse: r.AppPassword}
		ctx, annule := context.WithTimeout(context.Background(), time.Minute)
		compte, uid, err := n.utilisateur(ctx, id)
		annule()
		if err != nil {
			fin(err.Error())
			return
		}
		id.UserID = uid
		if err := n.auth.ConnecterNextcloud(id, compte); err != nil {
			fin(err.Error())
			return
		}
		fin("")
		return
	}
	fin("connexion Nextcloud expirée, recommencer")
}

// EtatConnexion : "" (rien en cours), "attente", "ok" ou un message d'erreur.
func (n *Nextcloud) EtatConnexion() string {
	n.mu.Lock()
	defer n.mu.Unlock()
	switch {
	case n.connexion == nil:
		return ""
	case !n.connexion.fini:
		return "attente"
	case n.connexion.erreur == "":
		return "ok"
	}
	return n.connexion.erreur
}

func (n *Nextcloud) utilisateur(ctx context.Context, id IdentNextcloud) (*Compte, string, error) {
	var d struct {
		ID          string `json:"id"`
		DisplayName string `json:"displayname"`
		Email       string `json:"email"`
	}
	if err := n.ocsAvec(ctx, id, "GET", "/ocs/v2.php/cloud/user", nil, &d); err != nil {
		return nil, "", err
	}
	nom := d.DisplayName
	if nom == "" {
		nom = d.ID
	}
	return &Compte{Nom: nom, Email: d.Email}, d.ID, nil
}

// Deconnecter révoque le mot de passe d'application sur le serveur (au mieux).
func (n *Nextcloud) Deconnecter(ctx context.Context) {
	if id, err := n.auth.Nextcloud(); err == nil {
		_ = n.ocsAvec(ctx, id, "DELETE", "/ocs/v2.php/core/apppassword", nil, nil)
	}
	n.mu.Lock()
	n.connexion = nil
	n.mu.Unlock()
}

// --- Requêtes ---

type ErreurNextcloud struct {
	Statut  int
	Message string
}

func (e *ErreurNextcloud) Error() string {
	if e.Message == "" {
		return fmt.Sprintf("Nextcloud : erreur HTTP %d", e.Statut)
	}
	return "Nextcloud : " + e.Message
}

func (n *Nextcloud) requete(ctx context.Context, id IdentNextcloud, methode, u string, entete http.Header, corps io.Reader) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, methode, u, corps)
	if err != nil {
		return nil, err
	}
	req.SetBasicAuth(id.Login, id.MotDePasse)
	req.Header.Set("User-Agent", ncAgent)
	req.Header.Set("OCS-APIRequest", "true")
	for k, v := range entete {
		req.Header[k] = v
	}
	if l, ok := corps.(interface{ Len() int }); ok {
		req.ContentLength = int64(l.Len())
	}
	resp, err := n.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("serveur Nextcloud injoignable (connexion Internet ?) : %w", err)
	}
	if resp.StatusCode >= 300 {
		defer resp.Body.Close()
		return nil, erreurNextcloud(resp)
	}
	return resp, nil
}

func erreurNextcloud(resp *http.Response) error {
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	switch resp.StatusCode {
	case http.StatusUnauthorized:
		return ErrNonConnecte
	case http.StatusNotFound:
		return ErrIntrouvable
	case http.StatusPreconditionFailed:
		return ErrConflit
	}
	msg := ""
	var ocs struct {
		OCS struct {
			Meta struct{ Message string } `json:"meta"`
		} `json:"ocs"`
	}
	var dav struct {
		Message string `xml:"message"`
	}
	if json.Unmarshal(b, &ocs) == nil && ocs.OCS.Meta.Message != "" {
		msg = ocs.OCS.Meta.Message
	} else if xml.Unmarshal(b, &dav) == nil {
		msg = dav.Message
	}
	if resp.StatusCode == http.StatusInsufficientStorage {
		msg = "espace de stockage plein"
	}
	return &ErreurNextcloud{Statut: resp.StatusCode, Message: msg}
}

// ocs appelle l'API OCS (JSON) et décode ocs.data dans sortie.
func (n *Nextcloud) ocs(ctx context.Context, methode, chemin string, form url.Values, sortie any) error {
	id, err := n.auth.Nextcloud()
	if err != nil {
		return err
	}
	return n.ocsAvec(ctx, id, methode, chemin, form, sortie)
}

func (n *Nextcloud) ocsAvec(ctx context.Context, id IdentNextcloud, methode, chemin string, form url.Values, sortie any) error {
	u := n.base() + chemin
	if strings.Contains(u, "?") {
		u += "&format=json"
	} else {
		u += "?format=json"
	}
	var corps io.Reader
	h := http.Header{"Accept": {"application/json"}}
	if form != nil {
		corps = strings.NewReader(form.Encode())
		h.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	resp, err := n.requete(ctx, id, methode, u, h, corps)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if sortie == nil {
		return nil
	}
	var r struct {
		OCS struct {
			Data json.RawMessage `json:"data"`
		} `json:"ocs"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return fmt.Errorf("réponse Nextcloud illisible : %w", err)
	}
	return json.Unmarshal(r.OCS.Data, sortie)
}

// davURL : adresse WebDAV d'un chemin relatif à l'espace de la personne.
func (n *Nextcloud) davURL(id IdentNextcloud, chemin string) string {
	return n.base() + "/remote.php/dav/files/" + url.PathEscape(id.UserID) + "/" + cheminEncode(chemin)
}

func (n *Nextcloud) dav(ctx context.Context, methode, chemin string, entete http.Header, corps io.Reader) (*http.Response, error) {
	id, err := n.auth.Nextcloud()
	if err != nil {
		return nil, err
	}
	return n.requete(ctx, id, methode, n.davURL(id, chemin), entete, corps)
}

// --- Fichiers (WebDAV) ---

const ncPropfind = `<?xml version="1.0"?>
<d:propfind xmlns:d="DAV:" xmlns:oc="http://owncloud.org/ns">
 <d:prop><d:getlastmodified/><d:getcontentlength/><d:resourcetype/><d:getetag/><oc:fileid/><oc:size/></d:prop>
</d:propfind>`

type ncMultistatus struct {
	Reponses []struct {
		Href  string `xml:"href"`
		Props []struct {
			Statut string `xml:"status"`
			Prop   struct {
				Modif   string `xml:"getlastmodified"`
				Taille  string `xml:"getcontentlength"`
				TailleD string `xml:"size"`
				ETag    string `xml:"getetag"`
				FileID  string `xml:"fileid"`
				Type    struct {
					Collection *struct{} `xml:"collection"`
				} `xml:"resourcetype"`
			} `xml:"prop"`
		} `xml:"propstat"`
	} `xml:"response"`
}

// lister renvoie l'élément (profondeur 0) ou l'élément puis ses enfants (1).
func (n *Nextcloud) lister(ctx context.Context, chemin string, profondeur int) ([]Element, error) {
	id, err := n.auth.Nextcloud()
	if err != nil {
		return nil, err
	}
	resp, err := n.requete(ctx, id, "PROPFIND", n.davURL(id, chemin),
		http.Header{"Depth": {strconv.Itoa(profondeur)}, "Content-Type": {"application/xml"}}, strings.NewReader(ncPropfind))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var ms ncMultistatus
	if err := xml.NewDecoder(resp.Body).Decode(&ms); err != nil {
		return nil, fmt.Errorf("réponse WebDAV illisible : %w", err)
	}
	prefixe := "/remote.php/dav/files/" + id.UserID + "/"
	var els []Element
	for _, r := range ms.Reponses {
		href, err := url.PathUnescape(r.Href)
		if err != nil {
			continue
		}
		i := strings.Index(href, prefixe)
		if i < 0 {
			continue
		}
		rel := strings.Trim(href[i+len(prefixe):], "/")
		e := Element{ID: rel, Nom: path.Base(rel)}
		if dir := path.Dir(rel); dir != "." {
			e.Parent = &struct {
				ID string `json:"id"`
			}{ID: dir}
		}
		for _, ps := range r.Props {
			if !strings.Contains(ps.Statut, " 200 ") {
				continue
			}
			p := ps.Prop
			if p.Modif != "" {
				e.ModifLe, _ = http.ParseTime(p.Modif)
			}
			if p.Type.Collection != nil {
				e.Dossier = &struct{}{}
				e.Taille, _ = strconv.ParseInt(p.TailleD, 10, 64)
			} else {
				e.Taille, _ = strconv.ParseInt(p.Taille, 10, 64)
			}
			e.ETag = p.ETag
			if e.Dossier != nil {
				e.WebURL = n.base() + "/index.php/apps/files/?dir=" + url.QueryEscape("/"+rel)
			} else if p.FileID != "" {
				e.WebURL = n.base() + "/index.php/f/" + p.FileID
			}
		}
		els = append(els, e)
	}
	if len(els) == 0 {
		return nil, ErrIntrouvable
	}
	return els, nil
}

func (n *Nextcloud) Element(ctx context.Context, id string) (*Element, error) {
	els, err := n.lister(ctx, id, 0)
	if err != nil {
		return nil, err
	}
	return &els[0], nil
}

func (n *Nextcloud) Enfants(ctx context.Context, id string) ([]Element, error) {
	els, err := n.lister(ctx, id, 1)
	if err != nil {
		return nil, err
	}
	var enfants []Element
	for _, e := range els {
		if e.ID != strings.Trim(id, "/") {
			enfants = append(enfants, e)
		}
	}
	return enfants, nil
}

func (n *Nextcloud) existe(ctx context.Context, chemin string) (bool, error) {
	_, err := n.lister(ctx, chemin, 0)
	if errors.Is(err, ErrIntrouvable) {
		return false, nil
	}
	return err == nil, err
}

// nomLibre renvoie nom, ou « nom 2 », « nom 3 »… s'il est déjà pris.
func (n *Nextcloud) nomLibre(ctx context.Context, parent, nom string) (string, error) {
	ext := path.Ext(nom)
	if ext == nom {
		ext = ""
	}
	base := strings.TrimSuffix(nom, ext)
	for i := 1; i < 100; i++ {
		essai := nom
		if i > 1 {
			essai = fmt.Sprintf("%s %d%s", base, i, ext)
		}
		pris, err := n.existe(ctx, parent+"/"+essai)
		if err != nil {
			return "", err
		}
		if !pris {
			return essai, nil
		}
	}
	return "", errors.New("trop de fichiers portant ce nom")
}

func (n *Nextcloud) AssurerRacine(ctx context.Context) (*Element, error) {
	racine := n.auth.Config().DossierRacine
	e, err := n.Element(ctx, racine)
	if !errors.Is(err, ErrIntrouvable) {
		return e, err
	}
	resp, err := n.dav(ctx, "MKCOL", racine, nil, nil)
	if err != nil {
		return nil, err
	}
	resp.Body.Close()
	return n.Element(ctx, racine)
}

func (n *Nextcloud) CreerDossier(ctx context.Context, parentID, nom string) (*Element, error) {
	libre, err := n.nomLibre(ctx, parentID, nom)
	if err != nil {
		return nil, err
	}
	resp, err := n.dav(ctx, "MKCOL", parentID+"/"+libre, nil, nil)
	if err != nil {
		return nil, err
	}
	resp.Body.Close()
	return n.Element(ctx, parentID+"/"+libre)
}

func (n *Nextcloud) Renommer(ctx context.Context, id, nom string) (string, error) {
	ident, err := n.auth.Nextcloud()
	if err != nil {
		return id, err
	}
	dest := path.Dir(id) + "/" + nom
	resp, err := n.requete(ctx, ident, "MOVE", n.davURL(ident, id),
		http.Header{"Destination": {n.davURL(ident, dest)}, "Overwrite": {"F"}}, nil)
	if err != nil {
		return id, err
	}
	resp.Body.Close()
	return dest, nil
}

func (n *Nextcloud) Supprimer(ctx context.Context, id string) error {
	resp, err := n.dav(ctx, "DELETE", id, nil, nil)
	if errors.Is(err, ErrIntrouvable) {
		return nil
	}
	if err == nil {
		resp.Body.Close()
	}
	return err
}

func (n *Nextcloud) Envoyer(ctx context.Context, parentID, nom string, taille int64, contenu io.Reader) (*Element, error) {
	libre, err := n.nomLibre(ctx, parentID, nom)
	if err != nil {
		return nil, err
	}
	dest := parentID + "/" + libre
	if taille <= ncSeuilMorceaux {
		req := io.LimitReader(contenu, taille)
		ident, err := n.auth.Nextcloud()
		if err != nil {
			return nil, err
		}
		r, _ := http.NewRequestWithContext(ctx, "PUT", n.davURL(ident, dest), req)
		r.ContentLength = taille
		r.SetBasicAuth(ident.Login, ident.MotDePasse)
		r.Header.Set("User-Agent", ncAgent)
		resp, err := n.client.Do(r)
		if err != nil {
			return nil, fmt.Errorf("envoi interrompu : %w", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode >= 300 {
			return nil, erreurNextcloud(resp)
		}
		return n.Element(ctx, dest)
	}
	return n.envoyerParMorceaux(ctx, dest, taille, contenu)
}

// envoyerParMorceaux : envoi « chunking » Nextcloud (dossier temporaire dans
// uploads/, morceaux numérotés, puis MOVE de .file vers la destination).
func (n *Nextcloud) envoyerParMorceaux(ctx context.Context, dest string, taille int64, contenu io.Reader) (*Element, error) {
	ident, err := n.auth.Nextcloud()
	if err != nil {
		return nil, err
	}
	alea := make([]byte, 8)
	_, _ = rand.Read(alea)
	dossier := fmt.Sprintf("%s/remote.php/dav/uploads/%s/regie-%x", n.base(), url.PathEscape(ident.UserID), alea)
	resp, err := n.requete(ctx, ident, "MKCOL", dossier, nil, nil)
	if err != nil {
		return nil, err
	}
	resp.Body.Close()
	nettoyer := func() {
		if r, err := n.requete(context.Background(), ident, "DELETE", dossier, nil, nil); err == nil {
			r.Body.Close()
		}
	}
	tampon := make([]byte, ncTailleMorceau)
	var envoye int64
	for num := 1; envoye < taille; num++ {
		k, err := io.ReadFull(contenu, tampon)
		if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
			nettoyer()
			return nil, err
		}
		if k == 0 {
			nettoyer()
			return nil, errors.New("envoi interrompu : fichier incomplet")
		}
		resp, err := n.requete(ctx, ident, "PUT", fmt.Sprintf("%s/%05d", dossier, num), nil, bytes.NewReader(tampon[:k]))
		if err != nil {
			nettoyer()
			return nil, err
		}
		resp.Body.Close()
		envoye += int64(k)
	}
	resp, err = n.requete(ctx, ident, "MOVE", dossier+"/.file",
		http.Header{"Destination": {n.davURL(ident, dest)}, "OC-Total-Length": {strconv.FormatInt(taille, 10)}}, nil)
	if err != nil {
		nettoyer()
		return nil, err
	}
	resp.Body.Close()
	return n.Element(ctx, dest)
}

func (n *Nextcloud) cheminDonnees() string {
	return n.auth.Config().DossierRacine + "/" + fichierDonnees
}

func (n *Nextcloud) LireDonnees(ctx context.Context) ([]byte, string, error) {
	resp, err := n.dav(ctx, "GET", n.cheminDonnees(), nil, nil)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	etag := resp.Header.Get("ETag")
	if etag == "" {
		etag = resp.Header.Get("OC-ETag")
	}
	return b, etag, err
}

func (n *Nextcloud) EcrireDonnees(ctx context.Context, b []byte, etag string) error {
	if _, err := n.AssurerRacine(ctx); err != nil {
		return err
	}
	h := http.Header{"Content-Type": {"application/json"}}
	if etag == "" {
		h.Set("If-None-Match", "*")
	} else {
		h.Set("If-Match", etag)
	}
	resp, err := n.dav(ctx, "PUT", n.cheminDonnees(), h, bytes.NewReader(b))
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

// --- Partages (OCS) ---

type ncPartage struct {
	ID    json.Number `json:"id"`
	URL   string      `json:"url"`
	Token string      `json:"token"`
}

func droitNextcloud(ecriture bool) string {
	if ecriture {
		return strconv.Itoa(ncDroitEcriture)
	}
	return strconv.Itoa(ncDroitLecture)
}

func (n *Nextcloud) Partager(ctx context.Context, dossierID string, dest Destinataire, ecriture bool, message string) (*Partage, error) {
	mdp := motDePasseLisible()
	form := url.Values{
		"path":        {"/" + dossierID},
		"permissions": {droitNextcloud(ecriture)},
		"password":    {mdp},
		"note":        {message},
	}
	var p ncPartage
	var errMail error
	if !n.auth.Config().NCEnvoiManuel {
		// Partage par e-mail : Nextcloud envoie lui-même le lien.
		f := cloneValues(form)
		f.Set("shareType", strconv.Itoa(ncPartageEmail))
		f.Set("shareWith", dest.Email)
		f.Set("sendMail", "true")
		errMail = n.ocs(ctx, "POST", "/ocs/v2.php/apps/files_sharing/api/v1/shares", f, &p)
		if errMail == nil {
			return n.versPartage(p, mdp, false), nil
		}
		if errors.Is(errMail, ErrNonConnecte) {
			return nil, errMail
		}
	}
	// Lien personnel nommé, à transmettre depuis sa messagerie.
	f := cloneValues(form)
	f.Set("shareType", strconv.Itoa(ncPartageLien))
	f.Set("label", dest.Nom)
	if err := n.ocs(ctx, "POST", "/ocs/v2.php/apps/files_sharing/api/v1/shares", f, &p); err != nil {
		if errMail != nil {
			return nil, fmt.Errorf("%w (partage par e-mail : %v)", err, errMail)
		}
		return nil, err
	}
	return n.versPartage(p, mdp, true), nil
}

func (n *Nextcloud) versPartage(p ncPartage, mdp string, sansMail bool) *Partage {
	lien := p.URL
	if lien == "" && p.Token != "" {
		lien = n.base() + "/index.php/s/" + p.Token
	}
	return &Partage{ID: p.ID.String(), Lien: lien, MotDePasse: mdp, SansMail: sansMail}
}

func (n *Nextcloud) ChangerDroit(ctx context.Context, dossierID string, a Acces, _ bool, dest Destinataire, ecriture bool, _ string) (*Partage, error) {
	err := n.ocs(ctx, "PUT", "/ocs/v2.php/apps/files_sharing/api/v1/shares/"+url.PathEscape(a.PermissionID),
		url.Values{"permissions": {droitNextcloud(ecriture)}}, nil)
	if err != nil {
		return nil, err
	}
	return &Partage{ID: a.PermissionID, Lien: a.Lien, MotDePasse: a.MotDePasse, SansMail: a.SansMail}, nil
}

// Renvoyer : redemande l'envoi de l'e-mail à Nextcloud ; pour un lien
// personnel, l'interface propose de l'envoyer soi-même.
func (n *Nextcloud) Renvoyer(ctx context.Context, dossierID string, a Acces, dest Destinataire, _ string) (*Partage, error) {
	p := &Partage{ID: a.PermissionID, Lien: a.Lien, MotDePasse: a.MotDePasse, SansMail: true}
	if !a.SansMail {
		err := n.ocs(ctx, "POST", "/ocs/v2.php/apps/files_sharing/api/v1/shares/"+url.PathEscape(a.PermissionID)+"/send-email",
			url.Values{}, nil)
		if errors.Is(err, ErrNonConnecte) {
			return nil, err
		}
		p.SansMail = err != nil
	}
	return p, nil
}

func (n *Nextcloud) Retirer(ctx context.Context, dossierID string, a Acces, _ bool, _ Destinataire) error {
	err := n.ocs(ctx, "DELETE", "/ocs/v2.php/apps/files_sharing/api/v1/shares/"+url.PathEscape(a.PermissionID), nil, nil)
	if errors.Is(err, ErrIntrouvable) {
		return nil
	}
	return err
}

func cloneValues(v url.Values) url.Values {
	c := url.Values{}
	for k, x := range v {
		c[k] = append([]string(nil), x...)
	}
	return c
}

// motDePasseLisible : 3 groupes de 4 caractères sans ambiguïté (pas de 0/O,
// 1/l/I), avec majuscule, minuscule et chiffre, ex. « Kp7m-Qx3r-Tz9w ».
func motDePasseLisible() string {
	const (
		maj = "ABCDEFGHJKLMNPQRSTUVWXYZ"
		min = "abcdefghijkmnpqrstuvwxyz"
		chi = "23456789"
	)
	tire := func(s string) byte {
		i, _ := rand.Int(rand.Reader, big.NewInt(int64(len(s))))
		return s[i.Int64()]
	}
	var groupes []string
	for g := 0; g < 3; g++ {
		groupes = append(groupes, string([]byte{tire(maj), tire(min), tire(chi), tire(min)}))
	}
	return strings.Join(groupes, "-")
}
