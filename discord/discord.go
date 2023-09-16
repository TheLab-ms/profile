package discord

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
)

type DiscordUserData struct {
	ID        string `json:"id"`
	CreatedAt string `json:"created_at"`
	Username  string `json:"global_name"`
	Avatar    struct {
		URL string `json:"link"`
	} `json:"avatar"`
}

func GetDiscordUserData(id string) (*DiscordUserData, error) {
	url := "https://discordlookup.mesavirep.xyz/v1/user/" + id

	client := &http.Client{}
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
		return nil, fmt.Errorf("received non-OK HTTP status: %s", res.Status)
	}
	user := DiscordUserData{}

	if err := json.NewDecoder(res.Body).Decode(&user); err != nil {
		return nil, err
	}
	log.Println(user)
	return &user, nil
}
