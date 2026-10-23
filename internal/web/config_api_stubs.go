package web

import "net/http"

func (c *ConfigService) registerDelegationRoutes(route func(string, http.HandlerFunc)) {}
func (c *ConfigService) registerOpsRoutes(route func(string, http.HandlerFunc))        {}
func (c *ConfigService) handleConfigReloadAPI(w http.ResponseWriter, r *http.Request)  {}
