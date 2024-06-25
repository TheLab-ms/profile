package server

import (
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/TheLab-ms/profile"
	"github.com/TheLab-ms/profile/internal/chatbot"
	"github.com/TheLab-ms/profile/internal/datamodel"
	"github.com/TheLab-ms/profile/internal/payment"
	"github.com/TheLab-ms/profile/internal/reporting"
	qrcode "github.com/skip2/go-qrcode"
)

func (s *Server) newSignupViewHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		profile.Templates.ExecuteTemplate(w, "signup.html", map[string]any{"page": "signup"})
	}
}

func (s *Server) newProfileViewHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user, err := s.Keycloak.GetUser(r.Context(), getUserID(r))
		if err != nil {
			renderSystemError(w, "error while fetching user: %s", err)
			return
		}

		prices := payment.CalculateDiscounts(user, s.PriceCache.GetPrices())
		renderProfile(w, user, prices)
	}
}

func renderProfile(w io.Writer, user *datamodel.User, prices []*datamodel.PriceDetails) error {
	viewData := map[string]any{
		"page":            "profile",
		"user":            user,
		"prices":          prices,
		"migratedAccount": user.PaypalMetadata.TimeRFC3339.After(time.Time{}),
	}
	if user.StripeCancelationTime.After(time.Unix(0, 0)) {
		viewData["expiration"] = user.StripeCancelationTime.Format("01/02/06")
	}

	return profile.Templates.ExecuteTemplate(w, "profile.html", viewData)
}

func (s *Server) newFobQRHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user, err := s.Keycloak.GetUser(r.Context(), getUserID(r))
		if err != nil {
			renderSystemError(w, "error while getting user: %s", err)
			return
		}
		url := fmt.Sprintf("%s/admin/assign-fob?email=%s", s.Env.SelfURL, user.Email)

		png, err := qrcode.Encode(url, qrcode.Medium, 512)
		if err != nil {
			renderSystemError(w, "generating QR code: %s", err)
			return
		}

		w.Header().Add("Content-Type", "image/png")
		w.Write(png)
	}
}

func (s *Server) newDiscordLinkHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		discordUserID := r.URL.Query().Get("user")
		sig := chatbot.GenerateHMAC(discordUserID, s.Env.DiscordBotToken)
		if r.URL.Query().Get("sig") != sig {
			http.Error(w, "invalid signature", 400)
			return
		}

		user, err := s.Keycloak.GetUser(r.Context(), getUserID(r))
		if err != nil {
			renderSystemError(w, "error while getting user: %s", err)
			return
		}

		user.DiscordUserID, _ = strconv.ParseInt(discordUserID, 10, 0)
		err = s.Keycloak.WriteUser(r.Context(), user)
		if err != nil {
			renderSystemError(w, "error while updating user: %s", err)
			return
		}
		reporting.DefaultSink.Eventf(user.Email, "DiscordLinked", "member linked discord account %s", discordUserID)
		http.Redirect(w, r, "/", http.StatusTemporaryRedirect)
	}
}
