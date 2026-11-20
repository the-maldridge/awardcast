package server

import (
	"net/http"
	"strconv"

	"github.com/the-maldridge/webapp"
)

// pathID reads the {id} path parameter. A value that is not a number is bad
// input rather than a zero id: the previous version parsed "abc" into 0, and a
// request for /award/abc/delete then asked after the award that had not
// been created yet.
func pathID(r *http.Request) (uint, error) {
	id, err := webapp.ParamUint(r, "id")
	return uint(id), err
}

// formID reads the {id} path parameter for a route registered both with and
// without one: the add form and its submit have no id in the path, the edit
// form and its submit do. An absent parameter is therefore the add case, while
// a present but unparseable one is still bad input.
func formID(r *http.Request) (uint, error) {
	if webapp.URLParam(r, "id") == "" {
		return 0, nil
	}
	return pathID(r)
}

// formOptionalUint reads a select of foreign keys whose first option is
// "none" and submits an empty value. FormUint refuses exactly that value — a
// blank there is a required field left empty — and this field is the case the
// refusal was not written for: empty is the answer, it just means zero.
func formOptionalUint(r *http.Request, name string) (uint, error) {
	raw, err := webapp.FormString(r, name)
	if err != nil {
		return 0, err
	}
	if raw == "" {
		return 0, nil
	}
	v, err := strconv.ParseUint(raw, 10, 64)
	if err != nil {
		return 0, &webapp.BadInput{
			Name:   name,
			Value:  raw,
			Reason: "must be a non-negative integer",
		}
	}
	return uint(v), nil
}
