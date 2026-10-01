# Régie Partage — consignes pour Claude

Application développée par Frédéric Bourset pour une salle de spectacle : partage des documents de chaque projet avec les régisseurs via le SharePoint Microsoft 365 de la salle, ou via Nextcloud. Répondre en français, de façon concise.

- Exécutable Windows (et Mac pour essais, lanceur `mac/Lancer Régie Partage.command`) (Go, sans dépendance externe) : serveur 127.0.0.1:47820 + interface HTML/JS sans framework dans `web/` (go:embed).
- Connexion Microsoft Entra (code d'autorisation + PKCE, client public, redirection `http://localhost:47820/auth/retour`). Jeton chiffré DPAPI dans `%AppData%\RegiePartage`.
- Données métier (régisseurs, projets, accès) dans `<dossier racine>/_regie-partage.json` sur SharePoint, écrites avec If-Match (eTag) et rejeu en cas de conflit (`Store.Modifier`) : plusieurs postes simultanés.
- Fournisseurs derrière l'interface `Fournisseur` (fournisseur.go) : Microsoft (graph.go) et Nextcloud (nextcloud.go : WebDAV, OCS, Login Flow v2, partage par e-mail type 4 avec mot de passe, repli lien type 3 nommé). Identifiants d'éléments opaques (ID Graph / chemin Nextcloud).
- Partage Microsoft : invitation Graph nominative (`requireSignIn`, code par e-mail pour les externes), une permission par régisseur, retrait individuel. Un seul dossier commun par projet.
- Essai sans serveur : `DEMO=1 go test -run TestDemo -timeout 0` (faux SharePoint) ou `DEMO=nextcloud …` (faux Nextcloud ; `DEMO_SANS_MAIL=1`, `DEMO_DECONNECTE=1`), puis http://localhost:47820/.

## Livraison d'une version
1. `VERSION=X.Y.Z ./build.sh` (vet, tests, exe Windows, zip dans dist/).
2. Mettre à jour `VERSION` par défaut dans build.sh et l'historique ci-dessous.
3. Commit et push sur main, puis publier : tag annoté `vX.Y.Z` poussé, ou GitHub > Actions > Release > Run workflow avec la version (crée le tag). Le proxy des sessions Claude refuse les pushs de tags : utiliser alors le bouton.

## Historique
- v1.0.0 : régisseurs, projets, dossier SharePoint par projet, dépôt de documents, invitations lecture / lecture + dépôt, renvoi et retrait d'accès, guide administrateur.
- v1.0.1 : compte Microsoft personnel (tenant `consumers`, OneDrive perso, sans Sites.*) pour essais ; version Mac.
- v1.1.0 : Nextcloud (connexion par le navigateur, partage personnel par e-mail ou lien, mot de passe à transmettre par SMS ou copie).
- v1.1.1 : Nextcloud — option « J'envoie les liens moi-même » (lien type 3 + messagerie), `sendMail=true` explicite, bouton de secours quand l'e-mail Nextcloud n'arrive pas (Nextcloud peut ne rien envoyer sans erreur).
