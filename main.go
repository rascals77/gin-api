package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jinzhu/gorm"
	_ "github.com/mattn/go-sqlite3"
	"github.com/spf13/viper"
	"gopkg.in/go-playground/validator.v9"
)

// Configuration parameters
type Config struct {
	DBFile   string `validate:"required"`
	Port     int    `validate:"required,gt=1024"`
	LogFile  string `validate:"required"`
	DataDir  string `validate:"required"`
	ExecFile string `validate:"required"`
	TLSKey   string `validate:"required"`
	TLSCert  string `validate:"required"`
}

// Used for validation of some fields of the payload
type Data struct {
	Ticket string `validate:"required,alphanum"`
}

// Describes the data that is inserted into the SQLite database
type BuildInfo struct {
	Id   int    `gorm:"type:integer;primary_key;autoincrement"`
	Date string `gorm:"type:text;not null"`
	Data string `gorm:"type:text;not null"`
}

// IsNotExist Helper function to determine if a path does not exit
func IsNotExist(path string) bool {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		// not exist
		return true
	}
	// exist
	return false
}

// Initialize database
func InitDb() *gorm.DB {
	// Openning file
	db, err := gorm.Open("sqlite3", conf.DBFile)

	// Disable plural table names
	db.SingularTable(true)

	// Display SQL queries
	db.LogMode(true)

	// Error
	if err != nil {
		panic(err)
	}
	// Creating the table
	if !db.HasTable(&BuildInfo{}) {
		db.CreateTable(&BuildInfo{})
		db.Set("gorm:table_options", "ENGINE=InnoDB").CreateTable(&BuildInfo{})
	}

	return db
}

// Write headers for CORS
func Cors() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Writer.Header().Add("Access-Control-Allow-Origin", "*")
		c.Next()
	}
}

// If needed, set some headers
func OptionsUser(c *gin.Context) {
	c.Writer.Header().Set("Access-Control-Allow-Methods", "DELETE,POST,PUT")
	c.Writer.Header().Set("Access-Control-Allow-Headers", "Content-Type")
	c.Next()
}

// Verify authentication
func TokenAuthMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		token := c.Request.Header.Get("token")

		if token == "" {
			c.JSON(401, gin.H{"error": "API token required"})
			c.Abort()
			return
		}

		if token != os.Getenv("API_TOKEN") {
			c.JSON(401, gin.H{"error": "Invalid API token"})
			c.Abort()
			return
		}

		c.Next()
	}
}

// Middleware for handling POST data
func PostData(c *gin.Context) {
	// Get the request body
	body := new(bytes.Buffer)
	body.ReadFrom(c.Request.Body)

	// Convert the request body to JSON
	payload := new(bytes.Buffer)
	err := json.Compact(payload, body.Bytes())
	if err != nil {
		fmt.Println(err)
		c.JSON(452, gin.H{"error": "Could not convert POST data to JSON"})
		c.Abort()
		return
	}

	t := time.Now().UTC()
	now := fmt.Sprintf("%d-%02d-%02d %02d:%02d:%02d",
		t.Year(), t.Month(), t.Day(),
		t.Hour(), t.Minute(), t.Second())

	// Validate
	var tmp Data
	_ = json.Unmarshal(body.Bytes(), &tmp)
	err = validate.Struct(tmp)
	if err != nil {
		fmt.Printf("Validation error\n")
		var messages bytes.Buffer
		for _, err := range err.(validator.ValidationErrors) {
			if err.Tag() == "required" {
				fmt.Printf("Value for %s is required.\n", strings.ToLower(err.Field()))
				messages.WriteString(fmt.Sprintf("Value for %s is required. ", strings.ToLower(err.Field())))
			} else {
				fmt.Printf("Value for %s (%s) needs to be %s.\n", strings.ToLower(err.Field()), err.Value(), err.Tag())
				messages.WriteString(fmt.Sprintf("Value for %s (%s) needs to be %s. ", strings.ToLower(err.Field()), err.Value(), err.Tag()))
			}
		}
		errorMsg := fmt.Sprintf("Validation error. %s", strings.TrimSpace(messages.String()))
		c.JSON(453, gin.H{"error": errorMsg})
		c.Abort()
		return
	}

	// Convert the compacted payload to indented JSON
	jsonOut := new(bytes.Buffer)
	err = json.Indent(jsonOut, payload.Bytes(), "", "  ")
	if err != nil {
		fmt.Println(err)
		c.JSON(454, gin.H{"error": "Could not convert payload to jsonOut"})
		c.Abort()
		return
	}
	jsonOut.WriteString("\n")

	// Write json data to file
	outFile := fmt.Sprintf("%s/%s.json", conf.DataDir, tmp.Ticket)
	writeErr := ioutil.WriteFile(outFile, jsonOut.Bytes(), 0644)
	if writeErr != nil {
		errorMsg := fmt.Sprintf("Unable to create %s", outFile)
		c.JSON(455, gin.H{"error": errorMsg})
		return
	}

	// Connection to the database
	db := InitDb()
	// Close connection database
	defer db.Close()

	// Attempt to insert a record
	var info BuildInfo
	info.Data = payload.String()
	info.Date = now

	if info.Data != "" {
		// INSERT INTO "build_info" ("data") VALUES (info.Data);
		db.Create(&info)
		// Display success
		c.JSON(201, gin.H{"success": info})
	} else {
		// Display error
		c.JSON(456, gin.H{"error": "Fields are empty"})
		return
	}

	// Execute deployment
	jsonFile := fmt.Sprintf("JSON_FILE=%s", outFile)
	cmd := exec.Command(conf.ExecFile)
	cmd.Env = append(os.Environ(),
		jsonFile,
	)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Setpgid: true,
		Pgid:    0,
	}
	cmdErr := cmd.Start()
	if cmdErr != nil {
		log.Fatal(err)
	}

	// curl -i -X POST -H "Content-Type: application/json" -d "{ \"firstname\": \"Thea\", \"lastname\": \"Queen\" }" http://localhost:8080/api/v1/data
}

var (
	conf     Config
	validate *validator.Validate
)

func main() {
	configFile := flag.String("config", "", "Config file")
	flag.Parse()

	if *configFile == "" {
		log.Fatal("the -config parameter is required")
	}

	// Verify config file exists
	if IsNotExist(*configFile) {
		log.Fatalf("ERROR: config file %s does not exist.\n", *configFile)
	}

	fmt.Printf("Using config: %s\n\n", *configFile)

	viper.SetConfigFile(*configFile)
	if err := viper.ReadInConfig(); err != nil {
		log.Fatalf("Error reading config file, %s", err)
	}

	err := viper.Unmarshal(&conf)
	if err != nil {
		log.Fatalf("unable to decode into struct, %v", err)
	}

	validate = validator.New()
	err = validate.Struct(conf)
	if err != nil {
		for _, err := range err.(validator.ValidationErrors) {
			if err.Tag() == "required" {
				fmt.Printf("Value for %s is required.\n", strings.ToLower(err.Field()))
			} else if err.Tag() == "gt" {
				fmt.Printf("Value for %s (%d) needs to be greater than %s.\n", strings.ToLower(err.Field()), err.Value(), err.Param())
			} else {
				fmt.Printf("Value for %s (%s) needs to be %s.\n", strings.ToLower(err.Field()), err.Value(), err.Tag())
			}
		}
		log.Fatalf("Unable to continue due to validation error(s).")
	}

	// Verify files exist
	// NOTE: DBFile and LogFile get created, verify their parent directories exist
	dbFileDir := filepath.Dir(conf.DBFile)
	if IsNotExist(dbFileDir) {
		log.Fatalf("ERROR: directory %s for dbfile does not exist.", dbFileDir)
	}
	logFileDir := filepath.Dir(conf.LogFile)
	if IsNotExist(logFileDir) {
		log.Fatalf("ERROR: directory %s for logfile does not exist.", logFileDir)
	}
	if IsNotExist(conf.DataDir) {
		log.Fatalf("ERROR: directory %s for datadir does not exist.", conf.DataDir)
	}
	if IsNotExist(conf.ExecFile) {
		log.Fatalf("ERROR: file %s does not exist.\n", conf.ExecFile)
	}
	if IsNotExist(conf.TLSKey) {
		log.Fatalf("ERROR: file %s does not exist.\n", conf.TLSKey)
	}
	if IsNotExist(conf.TLSCert) {
		log.Fatalf("ERROR: file %s does not exist.\n", conf.TLSCert)
	}

	// Disable debug mode
	gin.SetMode(gin.ReleaseMode)

	// Color is not needed in log file
	gin.DisableConsoleColor()

	// Create stdout log
	logStdout, _ := os.OpenFile(conf.LogFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	gin.DefaultWriter = io.MultiWriter(logStdout)

	// Create a router with default logger and recovery (crash-free) middleware
	r := gin.Default()

	// Setup cross-origin resource sharing
	r.Use(Cors())

	// If defined, use API_TOKEN for auth
	if os.Getenv("API_TOKEN") != "" {
		r.Use(TokenAuthMiddleware())
	}

	v1 := r.Group("api/v1")
	{
		v1.POST("/data", PostData)
	}

	// Listen for requests
	listenOn := fmt.Sprintf(":%d", conf.Port)
	r.RunTLS(listenOn, conf.TLSCert, conf.TLSKey)
}
