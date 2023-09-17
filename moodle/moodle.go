package moodle

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"

	"github.com/TheLab-ms/profile/conf"
)

type Moodle struct {
	url   string
	token string
}

type User struct {
	ID              int    `json:"id"`
	UserName        string `json:"username"`
	FirstName       string `json:"firstname"`
	LastName        string `json:"lastname"`
	FullName        string `json:"fullname"`
	Email           string `json:"email"`
	Suspended       bool   `json:"suspended"`
	Confirmed       bool   `json:"confirmed"`
	ProfileImageURL string `json:"profileimageurl"`
}

func New(c *conf.Env) *Moodle {
	return &Moodle{url: c.MoodleURL, token: c.MoodleWSToken}
}

func (m *Moodle) GetUserByID(id string) (*User, error) {
	url := fmt.Sprintf("%s/webservice/rest/server.php?wstoken=%s&wsfunction=core_user_get_users_by_field&field=id&values[0]=%s&moodlewsrestformat=json", m.url, m.token, id)

	client := http.DefaultClient
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}

	res, err := client.Do(req)
	if err != nil {
		return nil, err
	}

	defer res.Body.Close()

	if res.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(res.Body)
		return nil, fmt.Errorf("received non-OK HTTP status %d: %s", res.StatusCode, body)
	}

	var users []User // Declare users as a slice of User
	if err := json.NewDecoder(res.Body).Decode(&users); err != nil {
		log.Println("Error decoding response. ", err)
		return nil, err
	}

	if len(users) == 0 {
		return nil, fmt.Errorf("no user found with ID %s", id)
	}

	return &users[0], nil // Return the first user in the array
}
