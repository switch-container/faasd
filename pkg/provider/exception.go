package provider

import (
	"github.com/pkg/errors"
)

var ErrPkgDirNotFind = errors.New("could not find app package directory")

var ErrNotFoundLambda = errors.New("[LambdaManager] do not found")

var ErrColdStartTooMuch = errors.New("cold start rate reach limitation")

var ErrMemoryNotEnough = errors.New("reach memory limitation")
