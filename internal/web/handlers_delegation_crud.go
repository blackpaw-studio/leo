package web

import "net/http"

// The delegation structure forms: each reads its inputs and hands them to the
// mutator the JSON routes use too (config_delegation.go).

func (s *Server) handleDelegationRoleAdd(w http.ResponseWriter, r *http.Request) {
	s.delegationMutation(w, r, addRole(r.FormValue("name"), r.FormValue("template")))
}

func (s *Server) handleDelegationRoleRename(w http.ResponseWriter, r *http.Request) {
	s.delegationMutation(w, r, renameRole(r.FormValue("name"), r.FormValue("new_name")))
}

func (s *Server) handleDelegationRoleDelete(w http.ResponseWriter, r *http.Request) {
	s.delegationMutation(w, r, deleteRole(r.FormValue("name")))
}

func (s *Server) handleDelegationProfileAdd(w http.ResponseWriter, r *http.Request) {
	s.delegationMutation(w, r, addProfile(r.FormValue("name")))
}

func (s *Server) handleDelegationProfileRename(w http.ResponseWriter, r *http.Request) {
	s.delegationMutation(w, r, renameProfile(r.FormValue("name"), r.FormValue("new_name")))
}

func (s *Server) handleDelegationProfileDelete(w http.ResponseWriter, r *http.Request) {
	s.delegationMutation(w, r, deleteProfile(r.FormValue("name")))
}

func (s *Server) handleDelegationProfileDuplicate(w http.ResponseWriter, r *http.Request) {
	s.delegationMutation(w, r, duplicateProfile(r.FormValue("name"), r.FormValue("new_name")))
}
