package server

import (
	"net/http"

	"github.com/TheLab-ms/profile/internal/reporting"
)

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
			w.Write([]byte(`<meta http-equiv="refresh" content="3">Swipe your fob twice, then a new, unassigned fob...<br><i>Leave this tab open during the process!</i>`))
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

		err = s.Keycloak.EnableUserBuildingAccess(r.Context(), user, getUserID(r), fobID)
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

		err = s.Keycloak.ApplyDiscount(r.Context(), user, r.URL.Query().Get("type"))
		if err != nil {
			renderSystemError(w, "error while writing to Keycloak: %s", err)
			return
		}

		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte("Done!"))
	}
}
