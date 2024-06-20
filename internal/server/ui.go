package server

import (
	"io"
	"net/http"
	"strconv"
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
		etagString := r.URL.Query().Get("i")
		etag, _ := strconv.ParseInt(etagString, 10, 0)
		if etagString == "" {
			etag = -1
		}

		user, err := s.Keycloak.GetUserAtETag(r.Context(), getUserID(r), etag)
		if err != nil {
			renderSystemError(w, "error while fetching user: %s", err)
			return
		}

		prices := payment.CalculateDiscounts(user, s.PriceCache.GetPrices())
		renderProfile(w, user, prices, etag)
	}
}

func renderProfile(w io.Writer, user *keycloak.User, prices []*payment.Price, etag int64) error {
	viewData := map[string]any{
		"page":            "profile",
		"user":            user,
		"prices":          prices,
		"migratedAccount": user.LastPaypalTransactionTime != time.Time{},
		"stripePending":   etag != -1 && user.StripeETag < etag,
	}
	if user.StripeCancelationTime > 0 {
		viewData["expiration"] = time.Unix(user.StripeCancelationTime, 0).Format("01/02/06")
	}

	return profile.Templates.ExecuteTemplate(w, "profile.html", viewData)
}
