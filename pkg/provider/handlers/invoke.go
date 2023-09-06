package handlers

import (
	"fmt"
	"log"
	"net/http"
	"regexp"
	"strconv"

	"github.com/gorilla/mux"
	"github.com/openfaas/faas-provider/httputil"
	"github.com/pkg/errors"
)

// the function name pattern should be like (lalala-blalabla-512)
// the front letters forming the serviceName while the final digital numbers is id
var functionRegex = regexp.MustCompile(`([-a-zA-Z_0-9.]*)-(\d+)$`)

func ParseFunctionName(functionName string) (string, uint64, error) {
	var (
		serviceName string
		id          uint64
	)
	temp := functionRegex.FindStringSubmatch(functionName)
	if len(temp) != 3 {
		return serviceName, id, fmt.Errorf("invalid function name pattern: %s", functionName)
	}
	serviceName = temp[1]
	if tempId, err := strconv.Atoi(temp[2]); err == nil {
		id = uint64(tempId)
	} else {
		return serviceName, id, fmt.Errorf("function name %s do not contain id", functionName)
	}
	return serviceName, id, nil
}

func HookInvokeProxy(rawHandler http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		pathVars := mux.Vars(r)
		functionName := pathVars["name"]
		serviceName, id, err := ParseFunctionName(functionName)
		if err != nil {
			httputil.Errorf(w, http.StatusBadRequest, "Invalid function Name %s (%s)", functionName, err)
			return
		}
		_, err = lambdaManager.UpdateInstance(serviceName, id, func(info *ContainerInfo) error {
			if info.status != IDLE && info.status != FINISHED {
				return errors.Wrapf(ErrInstanceInvalidStatus,
					"weird instance status %s when invoke", info.status)
			}
			info.status = RUNNING
			return nil
		})
		if err != nil {
			httputil.Errorf(w, http.StatusBadRequest, "Invalid function Name %s %s", functionName, err)
			return
		}
		rawHandler(w, r)
		// since rawHandler may already close the ResponseWriter, we should not touch `w` below
		// so we only write logs for error
		_, err = lambdaManager.UpdateInstance(serviceName, id, func(info *ContainerInfo) error {
			if info.status != RUNNING {
				return errors.Wrapf(ErrInstanceInvalidStatus, "invoke %s post-update", functionName)
			}
			info.status = FINISHED
			return nil
		})
		if err != nil {
			log.Printf("ERROR %s", err)
		}
	}
}
