# Régie Partage

Partage des documents de projets avec les régisseurs, via le SharePoint Microsoft 365 de la salle. Les régisseurs reçoivent un lien par e-mail et un code de vérification : aucun compte à créer.

- Utilisation : `docs/LISEZMOI.txt`
- Mise en place Microsoft 365 : `docs/GUIDE-ADMINISTRATEUR.txt`
- Compilation : `VERSION=1.0.0 ./build.sh` → `dist/RegiePartage-1.0.0-windows.zip`
- Publication : GitHub > Actions > Release > Run workflow, avec le numéro de version (crée le tag et la Release avec le zip).
