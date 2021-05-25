// +build mage

package main

import (
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/aws/aws-sdk-go/aws/session"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/awserr"
	"github.com/aws/aws-sdk-go/aws/client"
	"github.com/aws/aws-sdk-go/service/s3"
	"github.com/caarlos0/env"
	"github.com/magefile/mage/mg" // mg contains helpful utility functions, like Deps
)

// Default - default target to run
var Default = Validate
var cfg config

const statusBadEnv = 1

func init() {
	err := env.Parse(&cfg)
	if err != nil {
		fmt.Printf("failed to parse environment variables: %v", err)
		os.Exit(statusBadEnv)
	}
	// If the current appenv is not production, set it to development
	if !strings.EqualFold(cfg.AppEnv, "production") {
		cfg.AppEnv = "development"
	}

	// Exports all variables prefixed with the value of AppEnv (as upper)
	// and trims the AppEnv_ value from the name
	err = exportEnv(cfg.AppEnv)
	if err != nil {
		fmt.Println(err)
		os.Exit(statusBadEnv)
	}

	// we need to discard *config so we can read even the
	// previously environment prefixed variables
	cfg = config{}
	err = env.Parse(&cfg)
	if err != nil {
		fmt.Printf("failed to parse environment variables: %v", err)
		os.Exit(statusBadEnv)
	}
	// If the current appenv is not production, set it to development
	if !strings.EqualFold(cfg.AppEnv, "production") {
		cfg.AppEnv = "development"
	}
	cfg.Export()
}

func exportEnv(appenv string) error {
	env := strings.ToUpper(appenv)

	for _, e := range os.Environ() {
		parts := strings.SplitN(e, "=", 2)

		// For maximum readability
		key := parts[0]
		value := parts[1]

		if !strings.HasPrefix(key, env+"_") {
			continue
		}
		err := os.Setenv(key[len(env)+1:], value)
		if err != nil {
			return fmt.Errorf("failed to read %s, when setting %s: %v", e, key[len(env)+1:], err)
		}
		fmt.Printf("Exported %s environment variable, value length=%d\n", key[len(env)+1:], len(value))
	}

	return nil
}

type config struct {
	ProjectName      string   `env:"projectName,required"`
	AlertEmail       string   `env:"alertEmail,required"`
	AppEnv           string   `env:"CIRCLE_BRANCH" envDefault:"sandbox"`
	Region           string   `env:"region" envDefault:"us-east-1"`
	SamlProviderArn  string   `env:"SAML_PROVIDER_ARN" envDefault:""`
	SecOpsAccountIDs []string `env:"secopsAccounts" envSeparator:","`
	InventoryVersion string   `env:"inventoryVersion" envDefault:"v0.1.7"`
	AnsibleVersion   string   `env:"ansibleVersion" envDefault:"v0.0.28"`
	NetworkVersion   string   `env:"networkVersion" envDefault:"v0.6.10"`
	SecretsVersion   string   `env:"secretsVersion" envDefault:"v0.0.3rc"`
}

func (c *config) Export() {
	vars := map[string]string{
		"TF_VAR_env":               c.AppEnv,
		"TF_VAR_email_address":     c.AlertEmail,
		"TF_VAR_project_name":      c.ProjectName,
		"TF_VAR_region":            c.Region,
		"TF_VAR_secops_accounts":   strings.Join(c.SecOpsAccountIDs, ","),
		"TF_VAR_saml_provider_arn": c.SamlProviderArn,
		"AWS_REGION":               c.Region,
	}
	for k, v := range vars {
		os.Setenv(k, v)
		fmt.Printf("Exported %s environment variable, value length=%d\n", k, len(v))
	}
}

// Test - test target
func Test() error {
	mg.Deps(Plan)
	return nil
}

// Deploy - deploy target
func Deploy() error {
	mg.Deps(Plan, Apply)
	return nil
}

// Apply - target for terraform apply
func Apply() error {
	mg.Deps(InitBackend)
	mg.Deps(Validate)
	defer func() {
		err := SaveBackend()
		if err != nil {
			fmt.Printf("failed to save backend: %v", err)
		}
	}()

	if !fileExists("plan.tfplan") {
		return mg.Fatalf(1, "failed to locate the terraform plan")
	}
	return run(map[string][]string{
		"terraform": {"apply", "--auto-approve", "plan.tfplan"},
	})
}

// GetRoles - target to get required Ansible roles
func GetRoles() error {
	return run(map[string][]string{
		"ansible-galaxy": {"install", "-r", "../ansible/requirements.yml", "--roles-path", "../ansible/roles"},
	})
}

// UploadAnsible - target to upload Ansible files to S3
func UploadAnsible() error {
	mg.Deps(GetRoles)
	src := "../ansible/"
	dst := strings.ToLower(fmt.Sprintf("s3://%s-%s-ansible-lambda/ansible/", cfg.ProjectName, cfg.AppEnv))
	return run(map[string][]string{
		"aws": {"s3", "cp", "--region", cfg.Region, "--recursive", src, dst},
	})
}

// Plan - target for terraform plan
func Plan() error {
	mg.Deps(InitBackend)
	mg.Deps(Validate)
	return run(map[string][]string{
		"terraform": []string{"plan", "-out", "plan.tfplan"},
	})
}

// Validate - target to validate terraform
func Validate() error {
	mg.Deps(Init)
	return run(map[string][]string{
		"terraform": []string{"validate"},
	})
}

// Init - target to initialize terraform
func Init() error {
	mg.Deps(DownloadInventory, DownloadAnsible, DownloadRotateKeyPair, DownloadNetwork, DownloadSecrets)
	return run(map[string][]string{
		"terraform": []string{"init"},
	})
}

// InitBackend - target to initialize the terraform backend S3 bucket
func InitBackend() error {
	sess, err := session.NewSession(&aws.Config{Region: &cfg.Region})
	if err != nil {
		return fmt.Errorf("failed to connect to AWS: %v", err)
	}
	bucket := fmt.Sprintf("%s-%s-backend", cfg.ProjectName, cfg.AppEnv)
	tfstate := fmt.Sprintf("%s.tfstate", bucket)

	local := []byte(fmt.Sprintf(`terraform {
	backend "local" {
		path = "%s"
	}
}`, tfstate))

	remote := []byte(fmt.Sprintf(`terraform {
	backend "s3" {
		bucket = "%s"
		key    = "%s"
		region = "%s"
	}
}`, bucket, tfstate, cfg.Region))

	var backend = remote
	if !bucketExists(sess, bucket) {
		backend = local
	}

	err = ioutil.WriteFile("backend.tf", backend, 0644)
	if err != nil {
		return fmt.Errorf("failed to write backend.tf: %v", err)
	}
	return nil
}

// SaveBackend - target to save tfstate to S3 backend
func SaveBackend() error {
	tfstate := fmt.Sprintf("%s-%s-backend.tfstate", cfg.ProjectName, cfg.AppEnv)
	if _, err := os.Stat(tfstate); os.IsNotExist(err) {
		// we did not write anything to the local tfstate file
		// this must not be the first time we ran
		return nil
	}
	sess, err := session.NewSession(&aws.Config{Region: &cfg.Region})
	if err != nil {
		return fmt.Errorf("failed to connect to AWS: %v", err)
	}
	bucket := fmt.Sprintf("%s-%s-backend", cfg.ProjectName, cfg.AppEnv)
	if bucketExists(sess, bucket) {
		err := uploadTerraformState(sess, bucket, "", tfstate)
		if err != nil {
			return fmt.Errorf("failed to upload terraform state to remote bucket: %s -> %v", bucket, err)
		}
	}
	// TODO: We should probably handle the scenario where the bucket does not exist
	// for example after a failed execution of terraform, we need to terraform destroy
	// the resources deployed to prevent an inconsistent environment
	return nil
}

const invUrlf = "https://github.com/GSA/grace-inventory/releases/download/%s/grace-inventory-lambda.zip"
const ansibleUrlf = "https://github.com/GSA/grace-ansible-lambda/releases/download/%s/grace-ansible-lambda.zip"
const rotateKeyPairUrlf = "https://github.com/GSA/grace-ansible-lambda/releases/download/%s/grace-ansible-rotate-keypair.zip"
const networkLambdaf = "https://github.com/GSA/grace-paas-network/releases/download/%s/grace-paas-associate-zone.zip"
const secretsLambdaf = "https://github.com/GSA/grace-secrets-sync-lambda/releases/download/%s/grace-secrets-sync-lambda.zip"

// DownloadInventory - target to download lamba executable
func DownloadInventory() error {
	path, err := createReleasePath("grace-inventory-lambda.zip")
	if err != nil {
		return err
	}

	return download(fmt.Sprintf(invUrlf, cfg.InventoryVersion), path)
}

// DownloadAnsible - target to download lamba executable
func DownloadAnsible() error {
	path, err := createReleasePath("grace-ansible-lambda.zip")
	if err != nil {
		return err
	}

	return download(fmt.Sprintf(ansibleUrlf, cfg.AnsibleVersion), path)
}

// DownloadRotateKeyPair - target to download lamba executable
func DownloadRotateKeyPair() error {
	path, err := createReleasePath("grace-ansible-rotate-keypair.zip")
	if err != nil {
		return err
	}

	return download(fmt.Sprintf(rotateKeyPairUrlf, cfg.AnsibleVersion), path)
}

// DownloadNetwork - target to download lamba executable
func DownloadNetwork() error {
	path, err := createReleasePath("grace-paas-associate-zone.zip")
	if err != nil {
		return err
	}

	return download(fmt.Sprintf(networkLambdaf, cfg.NetworkVersion), path)
}

// DownloadSecrets - target to download lamba executable
func DownloadSecrets() error {
	path, err := createReleasePath("grace-secrets-sync-lambda.zip")
	if err != nil {
		return err
	}

	return download(fmt.Sprintf(secretsLambdaf, cfg.SecretsVersion), path)
}

func createReleasePath(outfile string) (string, error) {
	wd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("failed to get working directory: %v", err)
	}

	dir := filepath.Join(wd, "release")

	err = os.Mkdir(dir, os.ModePerm)
	if err != nil && !os.IsExist(err) {
		return "", fmt.Errorf("failed to create release directory: %v", err)
	}
	return filepath.Join(dir, outfile), nil
}

func download(uri string, path string) error {
	u, err := url.Parse(uri)
	if err != nil {
		return fmt.Errorf("failed to parse inventory url: %v", err)
	}
	resp, err := http.Get(u.String())
	if err != nil {
		return fmt.Errorf("failed to download inventory zip: %v", err)
	}
	defer func() {
		err = resp.Body.Close()
		if err != nil {
			fmt.Printf("failed to close HTTP response body: %v", err)
		}
	}()
	return save(resp.Body, path)
}

func save(r io.Reader, path string) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0644)
	if err != nil {
		return fmt.Errorf("failed to open %s: %v", path, err)
	}
	defer func() {
		err := f.Close()
		if err != nil {
			fmt.Printf("failed to close %s file handle: %v", path, err)
		}
	}()
	_, err = io.Copy(f, r)
	if err != nil {
		return fmt.Errorf("failed to save inventory: %v", err)
	}
	return nil
}

func run(procs map[string][]string) error {
	for proc, args := range procs {
		cmd := exec.Command(proc, args...)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		err := cmd.Run()
		if err != nil {
			list := append([]string{proc}, args...)
			return fmt.Errorf("failed when executing: %v -> %v", list, err)
		}
	}
	return nil
}

func bucketExists(cfg client.ConfigProvider, bucket string) bool {
	svc := s3.New(cfg)
	_, err := svc.HeadBucket(&s3.HeadBucketInput{Bucket: &bucket})
	if err != nil {
		if v, ok := err.(awserr.Error); ok {
			if v.Code() == s3.ErrCodeNoSuchBucket {
				return false
			}
		}
		log.Printf("failed to call HeadBucket: %s -> %v", bucket, err)
		return false
	}
	return true
}

func fileExists(path string) bool {
	_, err := os.Stat(path)

	// return the negative of is not exist
	return !os.IsNotExist(err)
}

func uploadTerraformState(cfg client.ConfigProvider, bucket string, prefix string, path string) error {
	svc := s3.New(cfg)
	f, err := os.OpenFile(path, os.O_RDONLY, 0600)
	if err != nil {
		return fmt.Errorf("failed to open file: %s -> %v", path, err)
	}
	defer func() {
		err := f.Close()
		if err != nil {
			log.Printf("failed to close file handle for %s -> %v", path, err)
		}
	}()
	_, err = svc.PutObject(&s3.PutObjectInput{
		Key:    aws.String(fmt.Sprintf("%s/%s", prefix, filepath.Base(path))),
		Bucket: &bucket,
		Body:   f,
	})
	return err
}
