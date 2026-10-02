package client

// APIKey represents an API key
type APIKey struct {
	ID                  string `json:"id,omitempty"`
	Name                string `json:"name"`
	Key                 string `json:"key,omitempty"`
	Description         string `json:"description,omitempty"`
	ExpiresAt           string `json:"expiresAt"`
	LastUsedAt          string `json:"lastUsedAt,omitempty"`
	CreatedAt           string `json:"createdAt,omitempty"`
	ExpirationEmailSent bool   `json:"expirationEmailSent"`
}
