package models

type User struct {
	ID                 string `db:"id" json:"id"`
	FullName           string `db:"full_name" json:"full_name"`
	Email              string `db:"email" json:"email"`
	PhoneNumber        string `db:"phone_number" json:"phone_number"`
	IDTag              string `db:"id_tag" json:"id_tag"`
	AuthProvider       string `db:"auth_provider" json:"auth_provider"`
	IsEmailVerified    bool   `db:"is_email_verified" json:"is_email_verified"`
	IsPhoneVerified    bool   `db:"is_phone_verified" json:"is_phone_verified"`
	Status             string `db:"status" json:"status"`
	Timezone           string `db:"timezone" json:"timezone"`
	AuthToken          string `db:"auth_token" json:"-"`
	AuthTokenExpiresAt string `db:"auth_token_expires_at" json:"-"`
	CreatedAt          string `db:"created_at" json:"created_at"`
	UpdatedAt          string `db:"updated_at" json:"updated_at"`
}
