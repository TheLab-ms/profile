package main

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"io"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Nerzal/gocloak/v13"
	"github.com/kelseyhightower/envconfig"

	"github.com/TheLab-ms/profile/internal/conf"
	"github.com/TheLab-ms/profile/internal/keycloak"
)

func main() {
	env := &conf.Env{}
	if err := envconfig.Process("", env); err != nil {
		panic(err)
	}
	kc := keycloak.New(env)
	ctx := context.Background()

	token, err := kc.GetToken(ctx)
	if err != nil {
		panic(err)
	}

	dir := "hack/paypal-migration/data"
	files, err := os.ReadDir(dir)
	if err != nil {
		panic(err)
	}

	data := map[string][]payment{}
	for _, file := range files {
		fp := filepath.Join(dir, file.Name())

		func() {
			file, err := os.Open(fp)
			if err != nil {
				panic(err)
			}
			defer file.Close()

			r := csv.NewReader(file)
			r.Comma = ','
			r.LazyQuotes = true
			for i := 0; true; i++ {
				record, err := r.Read()
				if errors.Is(err, io.EOF) {
					break
				}
				if err != nil {
					panic(err)
				}
				if l := len(record); l < 18 {
					panic("got a line without enough elements")
				}

				if i == 0 {
					continue // skip header
				}

				ts, err := time.ParseInLocation("1/2/2006 15:04:05", record[0]+" "+record[1], time.Now().Location())
				if err != nil {
					panic(err)
				}

				p := payment{
					TimeRFC3339:   ts.Format(time.RFC3339),
					TransactionID: record[17],
					timeMS:        ts.Unix(),
				}
				p.Price, err = strconv.ParseFloat(strings.ReplaceAll(record[5], ",", ""), 64)
				if err != nil {
					panic(err)
				}
				data[record[10]] = append(data[record[10]], p)
			}
		}()
	}

	for email, s := range data {
		sort.Slice(s, func(i, j int) bool { return s[i].timeMS > s[j].timeMS })
		js, err := json.Marshal(&s[0])
		if err != nil {
			panic(js)
		}

		users, err := kc.Client.GetUsers(ctx, token.AccessToken, env.KeycloakRealm, gocloak.GetUsersParams{Email: &email})
		if err != nil {
			panic(js)
		}
		if len(users) == 0 {
			log.Printf("no user found for email %s", email)
			continue
		}
		user := users[0]
		if user.Attributes == nil {
			user.Attributes = &map[string][]string{}
		}
		a := *user.Attributes
		if len(a["paypalMigrationMetadata"]) > 0 && a["paypalMigrationMetadata"][0] == string(js) {
			log.Printf("%s is already in sync", email)
			continue
		}
		a["paypalMigrationMetadata"] = []string{string(js)}

		err = kc.Client.UpdateUser(ctx, token.AccessToken, env.KeycloakRealm, *user)
		if err != nil {
			panic(err)
		}
		log.Printf("updated user %s", email)
	}
}

type payment struct {
	Price         float64
	TimeRFC3339   string
	TransactionID string
	timeMS        int64
}
