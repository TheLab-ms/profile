package server

import (
	"encoding/csv"
	"log"
	"net/http"
	"strconv"
	"time"

	"github.com/TheLab-ms/profile/internal/reporting"
)

func (s *Server) newAdminDumpHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		users, err := s.Keycloak.ListUsers(r.Context())
		if err != nil {
			log.Printf("error while listing users: %s", err)
			w.WriteHeader(500)
			return
		}

		w.Header().Add("Content-Disposition", `"attachment; filename="export.csv"`)
		cw := csv.NewWriter(w)
		cw.Write([]string{
			"First", "Last", "Email", "Email Verified", "Waiver Signed",
			"Payment Status", "Building Access Enabled", "Discount Type", "Keyfob ID",
			"Signup Timestamp", "Last Visit Timestamp",
		})

		for _, user := range users {
			cw.Write([]string{
				user.First, user.Last, user.Email,
				strconv.FormatBool(user.EmailVerified), strconv.FormatBool(user.WaiverState == "Signed"),
				user.PaymentStatus(), strconv.FormatBool(user.ActiveMember),
				user.DiscountType, strconv.Itoa(user.FobID),
				user.SignupTime.Format(time.RFC3339), user.LastSwipeTime.Format(time.RFC3339),
			})
		}
		cw.Flush()
	}
}

func (s *Server) newAssignFobHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user, err := s.Keycloak.GetUserByEmail(r.Context(), r.URL.Query().Get("email"))
		if err != nil {
			renderSystemError(w, "error while getting user: %s", err)
			return
		}

		granter, err := s.Keycloak.GetUser(r.Context(), getUserID(r))
		if err != nil {
			renderSystemError(w, "error while getting user: %s", err)
			return
		}

		fobID, ok, err := reporting.DefaultSink.LastFobAssignment(r.Context(), granter.FobID)
		if err != nil {
			renderSystemError(w, "error while checking for fob swipes: %s", err)
			return
		}
		if !ok {
			w.Header().Set("Content-Type", "text/html")
			w.Write([]byte(`<meta http-equiv="refresh" content="3">Swipe your fob, then a new / unassigned fob...<br><br><i>Leave this tab open during the process!</i>`))
			return
		}

		exists, err := s.Keycloak.BadgeIDInUse(r.Context(), fobID)
		if err != nil {
			renderSystemError(w, "error while checking for fob assignment: %s", err)
			return
		}
		if exists {
			w.Header().Set("Content-Type", "text/html")
			w.Write([]byte(`That fob has already been assigned to another member!`))
			return
		}

		user.BuildingAccessApprover = getUserID(r)
		user.FobID = fobID
		err = s.Keycloak.WriteUser(r.Context(), user)
		if err != nil {
			renderSystemError(w, "error while writing to Keycloak: %s", err)
			return
		}
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte(`Done!`))
	}
}

func (s *Server) newApplyDiscountHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user, err := s.Keycloak.GetUserByEmail(r.Context(), r.URL.Query().Get("email"))
		if err != nil {
			renderSystemError(w, "error while getting user: %s", err)
			return
		}

		user.DiscountType = r.URL.Query().Get("type")
		err = s.Keycloak.WriteUser(r.Context(), user)
		if err != nil {
			renderSystemError(w, "error while writing to Keycloak: %s", err)
			return
		}

		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte("Done!"))
	}
}
