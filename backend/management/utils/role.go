package utils

import (
	"context"

	"github.com/google/uuid"
	ent "github.com/pyck-ai/pyck/backend/management/ent/gen"
	"github.com/pyck-ai/pyck/backend/management/ent/gen/role"
)

// GetRoleIDsFromNames konvertiert Rollennamen zu Role-Entity-IDs
// Erstellt automatisch neue Role-Entitäten für unbekannte Rollennamen
func GetRoleIDsFromNames(ctx context.Context, tx *ent.Tx, roleNames []string) ([]uuid.UUID, error) {
	var roleIDs []uuid.UUID
	for _, roleName := range roleNames {
		existingRole, err := tx.Role.Query().
			Where(role.Name(roleName)).
			Only(ctx)
		if err != nil {
			// Role existiert nicht, erstelle sie
			newRole, err := tx.Role.Create().
				SetName(roleName).
				SetDescription("Zitadel role: " + roleName).
				Save(ctx)
			if err != nil {
				return nil, err
			}
			roleIDs = append(roleIDs, newRole.ID)
		} else {
			roleIDs = append(roleIDs, existingRole.ID)
		}
	}
	return roleIDs, nil
}
