package main

import (
	"bufio"
	"context"
	"fmt"
	"github.com/olivere/elastic"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"
)

const (
	INDEX = "library"
)

const MAPPINGS = `
{
   "mappings": {
		"book": {
			"properties": {
				 "title": {
					   "type": "keyword"
				 },
				 "author": {
					   "type": "keyword"
				 },
				 "location": {
					   "type": "integer"
				},
				"text": {
						"type": "text"
				}
			}
		}
   }
}
`

type ParseBook struct {
	title      string
	author     string
	paragraphs []string
}

type RequestData struct {
	Location int64  `json:"location"`
	Text     string  `json:"text"`
	Title    string  `json:"title"`
	Author   string  `json:"author"`
}

func resetIndices(es *elastic.Client, ctx context.Context) {
	exist, err := es.IndexExists(INDEX).Do(ctx)
	if err != nil {
		log.Fatalf("IndexExists() ERROR: %v", err)
		// If the index exists..
	} else if exist {
		fmt.Println("The index " + INDEX + " already exists.")
		_, err = es.DeleteIndex(INDEX).Do(ctx)

		// Check for DeleteIndex() errors
		if err != nil {
			log.Fatalf("client.DeleteIndex() ERROR: %v", err)
		}
	}

	//Create Index
	create, err := es.CreateIndex(INDEX).Body(MAPPINGS).Do(ctx)
	if err != nil {
		log.Fatalf("CreateIndex() ERROR: %v", err)
	} else {
		fmt.Println("CreateIndex():", create)
	}
}

func isHealthy(es *elastic.Client, ctx context.Context) bool {
	healthDetails, err := es.ClusterHealth().Do(ctx)
	if err != nil {
		fmt.Errorf("Connection not healthy %v", err)
		return false
	}

	log.Print("%v", healthDetails)

	return true

}

func GetEnv(key, defaultValue string) string {
	value := os.Getenv(key)
	if len(value) == 0 {
		return defaultValue
	}
	return value
}

func getESDetails() *elastic.Client {
	url := "http://" + GetEnv("ES_HOST", "127.0.0.1") + ":9200"
	log.Print(url)

	// Instantiate a client instance of the elastic library
	client, err := elastic.NewClient(
		elastic.SetSniff(false),
		elastic.SetURL(url),
		elastic.SetHealthcheckInterval(5*time.Second), // quit trying after 5 seconds
	)
	if err != nil {
		fmt.Errorf("getESDetails failed %v", err)
		return nil
	}

	return client
}

func check(e error, filePath string) {
	if e != nil {
		fmt.Errorf("Error in reading file %s with error %v", filePath, e)
	}
}

func parseBookFile(filePath string) ParseBook {
	file, err := os.Open(filePath)
	if err != nil {
		log.Fatal(err)
	}
	defer file.Close()

	var author string
	var title string
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		txt := scanner.Text()
		if len(txt) > 6 && strings.HasPrefix(txt, "Title:") {
			title = txt[7:]
		}
		if len(txt) > 7 && strings.HasPrefix(txt, "Author:") {
			author = txt[8:]
			break
		}
	}

	if author == "" {
		author = "Unknown Author"
	}

	if err := scanner.Err(); err != nil {
		log.Fatal(err)
	}

	log.Printf(`Reading Book - %s By %s`, title, author)

	dat, err := ioutil.ReadFile(filePath)
	check(err, filePath)
	stringData := string(dat)

	startString := strings.ToUpper("*** START OF THIS PROJECT GUTENBERG EBOOK " + title + " ***")
	startInd := len("*** START OF THIS PROJECT GUTENBERG EBOOK "+title+" ***") + strings.Index(stringData, startString)
	if startInd == -1 {
		startString = strings.ToUpper("*** START OF THE PROJECT GUTENBERG EBOOK " + title + " ***")
		startInd = len("*** START OF THIS PROJECT GUTENBERG EBOOK "+title+" ***") + strings.Index(stringData, startString)
		if startInd == -1 {
			startString = strings.ToUpper("***START OF THE PROJECT GUTENBERG EBOOK " + title + "***")
			startInd = len("*** START OF THIS PROJECT GUTENBERG EBOOK "+title+" ***") + strings.Index(stringData, startString)
		}
	}

	endString := strings.ToUpper("*** END OF THIS PROJECT GUTENBERG EBOOK " + title + " ***")
	endInd := strings.Index(stringData, endString)
	if endInd == -1 {
		endString = strings.ToUpper("*** END OF THE PROJECT GUTENBERG EBOOK " + title + " ***")
		endInd = strings.Index(stringData, endString)
		if endInd == -1 {
			endString = strings.ToUpper("***END OF THE PROJECT GUTENBERG EBOOK " + title + "***")
			endInd = strings.Index(stringData, endString)
		}
	}

	slicedString := stringData[startInd:endInd]
	//log.Printf("%#s", slicedString)
	re := regexp.MustCompile(`\n\s+\n`)
	splittedString := re.Split(slicedString, -1)

	log.Printf(`Parsed %d Paragraphs`, len(splittedString))
	return ParseBook{
		title:      title,
		author:     author,
		paragraphs: splittedString,
	}
}

func insertBookData(es *elastic.Client, ctx context.Context, parseBook ParseBook) {
	bulk := es.
		Bulk().
		Index(INDEX).
		Type("book")

	for ind, val := range parseBook.paragraphs {
		doc := RequestData{
			Location: int64(ind),
			Title:    parseBook.title,
			Author:   parseBook.author,
			Text:     val,
		}
		d := elastic.NewBulkIndexRequest().Doc(doc)
		bulk.Add(d)

		if ind > 0 && ind%500 == 0 { // Do bulk insert in 500 paragraph batches
		log.Print(bulk.EstimatedSizeInBytes())
			if _, err := bulk.Do(ctx); err != nil {
				log.Println(err)
				fmt.Errorf("Bulk process failed")
				return
			}
			bulk = es.
				Bulk().
				Index(INDEX).
				Type("book")
			log.Printf(`Indexed Paragraphs %d - %d`, ind-499, ind)
		}
	}

	if _, err := bulk.Do(ctx); err != nil {
		log.Println(err)
		fmt.Errorf("Bulk process failed")
		return
	}
	log.Printf(`Indexed remaining Paragraphs`)
}

func readAndInsertBooks(es *elastic.Client, ctx context.Context) {
	isHealthy(es, ctx)
	resetIndices(es, ctx)

	files, err := ioutil.ReadDir("../books")
	if err != nil {
		log.Fatal(err)
	}

	for _, file := range files {
		log.Print(file.Name())
		if strings.HasSuffix(file.Name(), ".txt") {
			parseBook := parseBookFile("../books/" + file.Name())
			insertBookData(es, ctx, parseBook)
		}
	}
}

func responseDetails(w http.ResponseWriter, req *http.Request) {
	//w.Header().Set("Content-Type", "application/json")
	//fmt.Fprintf(w, healthDetails.String())
}

func main() {
	//http.HandleFunc("/health", responseDetails)
	//
	//http.ListenAndServe(":3000", nil)
	ctx := context.Background()
	es := getESDetails()
	readAndInsertBooks(es, ctx)
}
