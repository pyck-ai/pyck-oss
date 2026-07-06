package sdk

import "time"

type UserBase struct {
	ID        string
	LoginName string
}

type UserProfile struct {
	ID        string
	Username  string
	Email     string
	FirstName string
	LastName  string
}

type Project struct {
	ID string
}

type App struct {
	ID           string
	ClientID     string
	ClientSecret string
}

type Org struct {
	ID   string
	Name string
}

type Grant struct {
	ID string
}

type AppKey struct {
	ID   string
	JSON string
}

type PatToken struct {
	ID    string
	Token string
}

type JsonAppKeyInfo struct {
	ID         string
	Expiration time.Time
}
