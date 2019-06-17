package main

import (
	"crypto/sha1"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/lon9/inco"

	"github.com/hashicorp/go-version"
)

// BaseURL is url for factorio mod portal api
const BaseURL = "https://mods.factorio.com/api/mods"

// DownloadBaseURL is url for download mods
const DownloadBaseURL = "https://mods.factorio.com/%s?username=%s&token=%s"

// ModResult is result of factorio mod portal api
type ModResult struct {
	Results []Mod `json:"results"`
}

// Mod is a mod on server
type Mod struct {
	Title    string    `json:"title"`
	Name     string    `json:"name"`
	Releases []Release `json:"releases"`
}

// Release is releases of mods
type Release struct {
	DownloadURL string `json:"download_url"`
	FileName    string `json:"file_name"`
	Version     string `json:"version"`
	Sha1        string `json:"sha1"`
}

// LocalMod is struct for local momds
type LocalMod struct {
	Name     string
	FileName string
	Version  string
}

func main() {

	// Parse flags
	var (
		modDir          string
		composePath     string
		composeFilePath string
		serviceName     string
		username        string
		token           string
		isUpdateServer  bool
		webhookURL      string
	)
	flag.StringVar(&modDir, "d", "./data/mods", "Directory that located mods")
	flag.StringVar(&composePath, "c", "docker-compose", "docker-compose path")
	flag.StringVar(&composeFilePath, "f", "docker-compose.yml", "docker-compose.yml path")
	flag.StringVar(&serviceName, "s", "factorio", "service name of factorio image")
	flag.StringVar(&username, "u", "", "Username of factorio.com")
	flag.StringVar(&token, "t", "", "Token of your user")
	flag.BoolVar(&isUpdateServer, "server", false, "Update factorio server or not")
	flag.StringVar(&webhookURL, "w", "", "Webhook url for a notification")
	flag.Parse()

	// Searching local mods
	files, err := ioutil.ReadDir(modDir)
	if err != nil {
		panic(err)
	}
	localMods := make(map[string]*LocalMod)
	for _, file := range files {
		ext := path.Ext(file.Name())
		if ext == ".zip" {
			base := strings.ReplaceAll(file.Name(), ext, "")
			splitted := strings.Split(base, "_")
			modName := splitted[0]
			version := splitted[1]
			localMods[modName] = &LocalMod{
				Name:     modName,
				FileName: file.Name(),
				Version:  version,
			}
		}
	}

	// Getting mod info
	res, err := getModInfo(localMods)
	if err != nil {
		panic(err)
	}
	modInfo := make(map[string]*Mod)
	for i := range res.Results {
		modInfo[res.Results[i].Name] = &res.Results[i]
	}

	// Check update
	var isModUpdated bool
	for _, v := range localMods {
		localVersion, err := version.NewVersion(v.Version)
		if err != nil {
			panic(err)
		}
		newestMod, ok := modInfo[v.Name]
		if !ok {
			panic(err)
		}
		newestVersion, err := version.NewVersion(newestMod.Releases[len(newestMod.Releases)-1].Version)
		if err != nil {
			panic(err)
		}
		if !localVersion.Equal(newestVersion) {
			// New version detected
			log.Printf("New version of %s available %s -> %s\n", newestMod.Title, localVersion, newestVersion)
			// Download mod
			log.Printf("Downloading mod %s (%s)\n", newestMod.Title, newestVersion)
			if err := downloadMod(modDir, username, token, newestMod); err != nil {
				panic(err)
			}
			// Delete mod
			if err := deleteOldMod(modDir, v.FileName); err != nil {
				panic(err)
			}
			isModUpdated = true
			if err := inco.Incoming(webhookURL, &inco.Message{
				Text: fmt.Sprintf("Updated %s (%s -> %s)", newestMod.Title, localVersion, newestVersion),
			}); err != nil {
				panic(err)
			}
		}
	}

	// If mod was updated, server must be restarted
	if isModUpdated {
		if err := exec.Command(composePath, "-f", composeFilePath, "restart", serviceName).Run(); err != nil {
			panic(err)
		}
		// Wait for restart
		log.Println("Wait for restart.....")
		time.Sleep(5 * time.Second)
	}

	// Update server
	if isUpdateServer {
		if err := updateServer(composePath, composeFilePath, serviceName); err != nil {
			panic(err)
		}
		if err := inco.Incoming(webhookURL, &inco.Message{
			Text: "Server updated",
		}); err != nil {
			panic(err)
		}
	}
}

// Getting mod info from server.
func getModInfo(localMods map[string]*LocalMod) (*ModResult, error) {
	var v = url.Values{}
	for k := range localMods {
		v.Add("namelist", k)
	}
	log.Printf("Getting info from %s\n", BaseURL+"?"+v.Encode())
	res, err := http.Get(BaseURL + "?" + v.Encode())
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	decoder := json.NewDecoder(res.Body)
	if err != nil {
		return nil, err
	}
	var result ModResult
	if err := decoder.Decode(&result); err != nil {
		return nil, err
	}
	return &result, nil
}

func downloadMod(modDir, username, token string, mod *Mod) error {
	release := mod.Releases[len(mod.Releases)-1]
	u := fmt.Sprintf(DownloadBaseURL, release.DownloadURL, username, token)
	res, err := http.Get(u)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	b, err := ioutil.ReadAll(res.Body)
	if err != nil {
		return err
	}
	// Validating checksum
	if fmt.Sprintf("%x", sha1.Sum(b)) != release.Sha1 {
		return errors.New("Invalid file")
	}
	out, err := os.Create(filepath.Join(modDir, mod.Releases[len(mod.Releases)-1].FileName))
	if err != nil {
		return err
	}
	defer out.Close()
	// Setting owner to 845
	if err := out.Chown(845, 845); err != nil {
		return err
	}
	// Copying mod to file
	_, err = out.Write(b)
	return err
}

func deleteOldMod(modDir, fileName string) error {
	p := filepath.Join(modDir, fileName)
	return os.Remove(p)
}

// Update factorio server to latest version
func updateServer(composePath, composeFilePath, serviceName string) error {
	if err := exec.Command(composePath, "-f", composeFilePath, "pull", serviceName).Run(); err != nil {
		return err
	}
	return exec.Command(composePath, "-f", composeFilePath, "up", "-d", serviceName).Run()
}
