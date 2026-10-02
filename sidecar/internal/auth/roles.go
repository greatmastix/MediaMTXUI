package auth

import "fmt"

// Role is a UI role. Each role may do everything the roles below it may.
type Role string

// Roles, lowest first. A streamer sees only the streams it owns: it passes no viewer route, and the
// stream routes, open to every signed-in user, check ownership themselves.
const (
	RoleStreamer Role = "streamer"
	RoleViewer   Role = "viewer"
	RoleOperator Role = "operator"
	RoleAdmin    Role = "admin"
)

func (r Role) rank() int {
	switch r {
	case RoleStreamer:
		return 1
	case RoleViewer:
		return 2
	case RoleOperator:
		return 3
	case RoleAdmin:
		return 4
	default:
		return 0
	}
}

// AtLeast reports whether r includes floor. Unknown roles include nothing.
func (r Role) AtLeast(floor Role) bool { return r.rank() > 0 && r.rank() >= floor.rank() }

// ParseRole accepts admin, operator, viewer and streamer.
func ParseRole(s string) (Role, error) {
	r := Role(s)
	if r.rank() == 0 {
		return "", fmt.Errorf("unknown role %q", s)
	}
	return r, nil
}
