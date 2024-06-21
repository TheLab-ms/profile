package keycloak

import (
	"encoding/json"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/Nerzal/gocloak/v13"
)

func mapToUserType(kcuser *gocloak.User, user any) {
	rt := reflect.TypeOf(user).Elem()
	rv := reflect.ValueOf(user).Elem()

	for i := 0; i < rv.NumField(); i++ {
		ft := rt.Field(i)
		fv := rv.Field(i)
		tag := ft.Tag.Get("keycloak")
		if tag == "id" {
			fv.SetString(gocloak.PString(kcuser.ID))
		} else if tag == "first" {
			fv.SetString(gocloak.PString(kcuser.FirstName))
		} else if tag == "last" {
			fv.SetString(gocloak.PString(kcuser.LastName))
		} else if tag == "email" {
			fv.SetString(gocloak.PString(kcuser.Email))
		} else if tag == "emailVerified" {
			fv.SetBool(gocloak.PBool(kcuser.EmailVerified))
		}
		if !strings.HasPrefix(tag, "attr.") {
			continue
		}

		key := strings.TrimPrefix(tag, "attr.")
		val := safeGetAttr(kcuser, key)
		if val == "" {
			continue
		}
		tn := rv.Field(i).Type().String()
		switch tn {
		case "int", "int64":
			i, _ := strconv.ParseInt(val, 10, 0)
			fv.SetInt(i)
		case "bool":
			b, _ := strconv.ParseBool(val)
			fv.SetBool(b)
		case "string":
			fv.SetString(val)
		case "time.Time":
			i, _ := strconv.ParseInt(val, 10, 0)
			t := time.Unix(i, 0)
			fv.Set(reflect.ValueOf(t))
		default:
			v := reflect.New(ft.Type).Interface()
			json.Unmarshal([]byte(val), &v)
			fv.Set(reflect.ValueOf(v).Elem())
		}
	}
}

func mapFromUserType(kcuser *gocloak.User, user any) {
	rt := reflect.TypeOf(user).Elem()
	rv := reflect.ValueOf(user).Elem()

	attrs := safeGetAttrs(kcuser)
	for i := 0; i < rv.NumField(); i++ {
		ft := rt.Field(i)
		fv := rv.Field(i)
		tag := ft.Tag.Get("keycloak")
		if tag == "id" {
			kcuser.ID = gocloak.StringP(fv.Interface().(string))
		} else if tag == "first" {
			kcuser.FirstName = gocloak.StringP(fv.Interface().(string))
		} else if tag == "last" {
			kcuser.LastName = gocloak.StringP(fv.Interface().(string))
		} else if tag == "email" {
			kcuser.Email = gocloak.StringP(fv.Interface().(string))
		} else if tag == "emailVerified" {
			kcuser.EmailVerified = gocloak.BoolP(fv.Interface().(bool))
		}
		if !strings.HasPrefix(tag, "attr.") {
			continue
		}

		key := strings.TrimPrefix(tag, "attr.")
		switch val := fv.Interface().(type) {
		case int:
			attrs[key] = []string{strconv.Itoa(val)}
		case int64:
			attrs[key] = []string{strconv.FormatInt(val, 10)}
		case bool:
			attrs[key] = []string{strconv.FormatBool(val)}
		case string:
			attrs[key] = []string{val}
		case time.Time:
			attrs[key] = []string{strconv.FormatInt(val.Unix(), 10)}
		default:
			raw, _ := json.Marshal(&val)
			attrs[key] = []string{string(raw)}
		}
	}
}
