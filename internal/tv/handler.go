package tv

import (
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"klyvi-api/internal/platform/http/middleware"
	"klyvi-api/internal/platform/http/response"
)

type API struct {
	service *Service
}

func NewAPI(service *Service) *API {
	return &API{service: service}
}

func (a *API) GetTvById(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "id")
	id, err := strconv.Atoi(idStr)
	if err != nil {
		response.WriteError(w, http.StatusBadRequest, "invalid tv id")
		return
	}

	query := r.URL.Query()
	idType := query.Get("type")
	if idType == "" {
		response.WriteError(w, http.StatusBadRequest, "type is required (external | internal)")
		return
	}

	seasonNum := 0

	if idType == "external" {
		if s := query.Get("season_num"); s != "" {
			seasonNum, err = strconv.Atoi(s)
			if err != nil {
				response.WriteError(w, http.StatusBadRequest, "invalid season number")
				return
			}
		}
	}

	tv, err := a.service.GetTvById(r.Context(), idType, id, seasonNum)
	if err != nil {
		response.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}

	response.WriteSuccess(
		w,
		http.StatusOK,
		"v1",
		middleware.StatsFromContext(r.Context()),
		tv,
	)
}

func (a *API) GetTvRecommendations(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "id")
	id, err := strconv.Atoi(idStr)
	if err != nil {
		response.WriteError(w, http.StatusBadRequest, "invalid tv id")
		return
	}

	query := r.URL.Query()
	idType := query.Get("type")
	if idType == "" {
		response.WriteError(w, http.StatusBadRequest, "type is required (external | internal)")
		return
	}

	recommendations, err := a.service.GetTvRecommendations(r.Context(), idType, id)
	if err != nil {
		response.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}

	response.WriteSuccess(
		w,
		http.StatusOK,
		"v1",
		middleware.StatsFromContext(r.Context()),
		recommendations,
	)
}

func (a *API) GetTvCollection(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "id")
	id, err := strconv.Atoi(idStr)
	if err != nil {
		response.WriteError(w, http.StatusBadRequest, "invalid tv id")
		return
	}

	query := r.URL.Query()
	idType := query.Get("type")
	if idType == "" {
		response.WriteError(w, http.StatusBadRequest, "type is required (external | internal)")
		return
	}

	collection, err := a.service.GetTvCollection(r.Context(), idType, id)
	if err != nil {
		response.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}

	response.WriteSuccess(
		w,
		http.StatusOK,
		"v1",
		middleware.StatsFromContext(r.Context()),
		collection,
	)
}

func (a *API) GetTvList(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	listType := query.Get("type")

	data, err := a.service.GetTvList(listType)
	if err != nil {
		response.WriteError(w, http.StatusBadRequest, err.Error())
		return
	}

	response.WriteSuccess(
		w,
		http.StatusOK,
		"v1",
		middleware.StatsFromContext(r.Context()),
		data,
	)
}
