package std

import "github.com/golang-jwt/jwt/v5"

func IsValidJWT(token string) bool {
	_, _, err := jwt.NewParser().ParseUnverified(token, jwt.MapClaims{})
	return err == nil
}
