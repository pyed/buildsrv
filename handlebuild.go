package main

import (
	"errors"
	"io"
	"math/rand"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/caddyserver/buildsrv/features"
)

func handleBuild(w http.ResponseWriter, r *http.Request) {
	goOS := r.URL.Query().Get("os")
	goArch := r.URL.Query().Get("arch")
	goARM := r.URL.Query().Get("arm")
	featureList := strings.Split(r.URL.Query().Get("features"), ",")

	err := checkInput(goOS, goArch, goARM, featureList)
	if err != nil {
		handleError(w, r, err, http.StatusBadRequest)
		return
	}

	// Make sure build hashes won't end up different
	if goArch != "arm" {
		goARM = ""
	}

	// Put features in order to keep hashes consistent and for use in the codegen function
	var orderedFeatures features.Middlewares // TODO - could this be a []string instead? Would make things a little simpler, not needing that String() method
loop:
	for _, m := range features.Registry {
		for _, feature := range featureList {
			if feature == m.Directive {
				orderedFeatures = append(orderedFeatures, m)
				continue loop
			}
		}
	}

	// Create 'hash' to identify this build
	hash := buildHash(goOS, goArch, goARM, orderedFeatures.String())

	// Get the path from which to download the file
	buildsMutex.Lock()
	b, ok := builds[hash]
	buildsMutex.Unlock()

	if ok {
		// build exists; wait for it to complete if not done yet
		<-b.DoneChan
	} else {
		// no build yet; reserve it so we don't duplicate the build job
		ts := time.Now().Format("060201150405") // YearMonthDayHourMinSec
		var downloadPath string
		for {
			// find a suitable random number not already in use
			random := strconv.Itoa(rand.Intn(100) + 899)
			downloadPath = buildPath + "/" + ts + random
			_, err := os.Stat(downloadPath)
			if os.IsNotExist(err) {
				break
			}
		}

		buildFilename := "caddy_" + goOS + "_" + goArch + "_custom"
		downloadFilename := buildFilename + ".zip"
		if goOS == "windows" {
			buildFilename += ".exe"
		}

		b = Build{
			DoneChan:         make(chan struct{}),
			OutputFile:       downloadPath + "/" + buildFilename,
			DownloadFile:     downloadPath + "/" + downloadFilename,
			DownloadFilename: downloadFilename,
			GoOS:             goOS,
			GoArch:           goArch,
			GoARM:            goARM,
			Features:         orderedFeatures,
			Hash:             hash,
		}

		buildsMutex.Lock()
		builds[hash] = b // save the build
		buildsMutex.Unlock()

		// Perform build (blocking)
		err = build(b)
		if err != nil {
			handleError(w, r, err, http.StatusInternalServerError)
			return
		}
	}

	// Update our copy of the build information
	buildsMutex.Lock()
	b, ok = builds[hash]
	buildsMutex.Unlock()
	if !ok {
		handleError(w, r, errors.New("Build doesn't exist"), http.StatusInternalServerError)
		return
	}

	// Open download file
	f, err := os.Open(b.DownloadFile)
	if err != nil {
		handleError(w, r, err, http.StatusInternalServerError)
		return
	}
	defer f.Close()

	w.Header().Set("Location", buildBase+"/download/"+b.DownloadFile)
	w.Header().Set("Expires", b.Expires.Format(http.TimeFormat))
	w.Header().Set("Content-Disposition", "attachment; filename=\""+b.DownloadFilename+"\"")

	if ok {
		w.WriteHeader(http.StatusOK)
	} else {
		w.WriteHeader(http.StatusCreated)
	}

	io.Copy(w, f)
}
