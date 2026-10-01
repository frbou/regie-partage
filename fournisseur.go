package main

import (
	"context"
	"io"
)

// Fournisseur : service de stockage et de partage (Microsoft 365 ou Nextcloud).
// Les identifiants d'éléments sont propres à chaque fournisseur (identifiant
// Graph pour Microsoft, chemin pour Nextcloud) et ne sont jamais interprétés
// ailleurs.
type Fournisseur interface {
	AssurerRacine(ctx context.Context) (*Element, error)
	CreerDossier(ctx context.Context, parentID, nom string) (*Element, error)
	// Renommer renvoie le nouvel identifiant (inchangé chez Microsoft).
	Renommer(ctx context.Context, id, nom string) (string, error)
	Enfants(ctx context.Context, id string) ([]Element, error)
	Element(ctx context.Context, id string) (*Element, error)
	Supprimer(ctx context.Context, id string) error
	Envoyer(ctx context.Context, parentID, nom string, taille int64, contenu io.Reader) (*Element, error)

	// Fichier de données commun (eTag) ; ErrIntrouvable / ErrConflit.
	LireDonnees(ctx context.Context) ([]byte, string, error)
	EcrireDonnees(ctx context.Context, contenu []byte, etag string) error

	Partager(ctx context.Context, dossierID string, dest Destinataire, ecriture bool, message string) (*Partage, error)
	// ChangerDroit modifie un accès existant ; commun = permission partagée
	// avec d'autres invités (cas Microsoft).
	ChangerDroit(ctx context.Context, dossierID string, a Acces, commun bool, dest Destinataire, ecriture bool, message string) (*Partage, error)
	Renvoyer(ctx context.Context, dossierID string, a Acces, dest Destinataire, message string) (*Partage, error)
	Retirer(ctx context.Context, dossierID string, a Acces, commun bool, dest Destinataire) error
}

type Destinataire struct {
	Email string
	Nom   string
}

// Partage : résultat d'un partage.
type Partage struct {
	ID         string
	Lien       string // lien personnel (Nextcloud)
	MotDePasse string // à communiquer séparément (Nextcloud)
	SansMail   bool   // le service n'a pas envoyé d'e-mail : à envoyer soi-même
}

func (p *Partage) versAcces(regID string, ecriture bool, envoyeLe string) Acces {
	return Acces{RegisseurID: regID, Ecriture: ecriture, PermissionID: p.ID,
		Lien: p.Lien, MotDePasse: p.MotDePasse, SansMail: p.SansMail, EnvoyeLe: envoyeLe}
}

// --- Microsoft 365 ---

type fournisseurMicrosoft struct {
	g    *Graphe
	auth *Auth
}

func (m fournisseurMicrosoft) cheminDonnees() string {
	return m.auth.Config().DossierRacine + "/" + fichierDonnees
}

func (m fournisseurMicrosoft) AssurerRacine(ctx context.Context) (*Element, error) {
	return m.g.AssurerDossier(ctx, m.auth.Config().DossierRacine)
}

func (m fournisseurMicrosoft) CreerDossier(ctx context.Context, parentID, nom string) (*Element, error) {
	return m.g.CreerDossier(ctx, parentID, nom)
}

func (m fournisseurMicrosoft) Renommer(ctx context.Context, id, nom string) (string, error) {
	return id, m.g.Renommer(ctx, id, nom)
}

func (m fournisseurMicrosoft) Enfants(ctx context.Context, id string) ([]Element, error) {
	return m.g.Enfants(ctx, id)
}

func (m fournisseurMicrosoft) Element(ctx context.Context, id string) (*Element, error) {
	return m.g.Element(ctx, id)
}

func (m fournisseurMicrosoft) Supprimer(ctx context.Context, id string) error {
	return m.g.Supprimer(ctx, id)
}

func (m fournisseurMicrosoft) Envoyer(ctx context.Context, parentID, nom string, taille int64, r io.Reader) (*Element, error) {
	return m.g.Envoyer(ctx, parentID, nom, taille, r)
}

func (m fournisseurMicrosoft) LireDonnees(ctx context.Context) ([]byte, string, error) {
	return m.g.LireFichier(ctx, m.cheminDonnees())
}

func (m fournisseurMicrosoft) EcrireDonnees(ctx context.Context, b []byte, etag string) error {
	if _, err := m.AssurerRacine(ctx); err != nil {
		return err
	}
	_, err := m.g.EcrireFichier(ctx, m.cheminDonnees(), b, etag)
	return err
}

func (m fournisseurMicrosoft) Partager(ctx context.Context, dossierID string, dest Destinataire, ecriture bool, message string) (*Partage, error) {
	id, err := m.g.Inviter(ctx, dossierID, dest.Email, ecriture, true, message)
	if err != nil {
		return nil, err
	}
	return &Partage{ID: id}, nil
}

// Chez Microsoft, changer le droit = retirer puis réinviter (nouvel e-mail).
func (m fournisseurMicrosoft) ChangerDroit(ctx context.Context, dossierID string, a Acces, commun bool, dest Destinataire, ecriture bool, message string) (*Partage, error) {
	if err := m.Retirer(ctx, dossierID, a, commun, dest); err != nil {
		return nil, err
	}
	return m.Partager(ctx, dossierID, dest, ecriture, message)
}

func (m fournisseurMicrosoft) Renvoyer(ctx context.Context, dossierID string, a Acces, dest Destinataire, message string) (*Partage, error) {
	return m.Partager(ctx, dossierID, dest, a.Ecriture, message)
}

// SharePoint regroupe parfois plusieurs invités extérieurs sur un même lien :
// dans ce cas on ne retire que la personne, pas le lien commun.
func (m fournisseurMicrosoft) Retirer(ctx context.Context, dossierID string, a Acces, commun bool, dest Destinataire) error {
	if commun {
		return m.g.RetirerDuLien(ctx, dossierID, a.PermissionID, dest.Email)
	}
	return m.g.RetirerPermission(ctx, dossierID, a.PermissionID)
}
