package server

import (
	"net/http"

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
