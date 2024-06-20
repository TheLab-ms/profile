package server

import (
	"io"
	"net/http"
	"time"

	"github.com/TheLab-ms/profile"
	"github.com/TheLab-ms/profile/internal/keycloak"
	"github.com/TheLab-ms/profile/internal/payment"
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

func renderProfile(w io.Writer, user *keycloak.User, prices []*payment.Price) error {
	viewData := map[string]any{
		"page":            "profile",
		"user":            user,
		"prices":          prices,
		"migratedAccount": user.LastPaypalTransactionTime != time.Time{},
	}
	if user.StripeCancelationTime > 0 {
		viewData["expiration"] = time.Unix(user.StripeCancelationTime, 0).Format("01/02/06")
	}

	return profile.Templates.ExecuteTemplate(w, "profile.html", viewData)
}
