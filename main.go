package main

// https://github.com/pdfcpu/pdfcpu/issues/124#issuecomment-630320053

import (
	"encoding/csv"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"

	dlgs "github.com/gen2brain/dlgs"
	"github.com/pdfcpu/pdfcpu/pkg/api"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu"
	log "github.com/sirupsen/logrus"
)

type Fields struct {
	Name  string
	Value string
}

func main() {
	var message string
	var source string
	var title string
	var cmdLine = false

	initLogfile()

	if len(os.Args) == 2 {
		source = os.Args[1]
		cmdLine = true
	}
	if cmdLine { // let's trust dlgs to enforce selection as directory
		fi, err := os.Lstat(source)
		if err != nil {
			log.Fatal(err)
		}
		if !fi.Mode().IsDir() {
			log.Fatalf("%v is not a directory\n", source)
		}
	} else {
		source = getDirFromDialog()
		if source == "" {
			return
		}
	}

	data, files := readPdfs(source)
	structuredData := structData(data, files)
	filename, message := writeFile(source, structuredData)
	if filename != "" {
		title = "Success"
	} else {
		title = "There was a problem"
	}
	if cmdLine {
		fmt.Printf("%s\n%s\n", title, message)
	} else {
		_, err := dlgs.Info(title, message)
		if err != nil {
			log.Fatal(err)
		}
	}
}

func writeFile(source string, structuredData [][]string) (string, string) {
	var msg string
	filename := fmt.Sprintf("%s/pdfSummary-%s.csv",
		source,
		time.Now().Format("20060102T150405"),
	)
	outputFile, err := os.OpenFile(
		filename, os.O_WRONLY|os.O_CREATE, 0644)
	if err != nil {
		msg = fmt.Sprintf(
			"Cannot write summary file to %s: %v\n",
			filename, err)
		return "", msg
	}
	w := csv.NewWriter(outputFile)
	w.WriteAll(structuredData)
	if err := w.Error(); err != nil {
		msg = fmt.Sprintf("Error writing results file: %v", err)
		return "", msg
	}
	msg = fmt.Sprintf("Summary file written to:\n    %s\n", filename)
	return filename, msg
}

func structData(data map[string][]Fields, files []string) [][]string {
	// There must be a better way to do this... sorry
	var index int
	var table = make(map[string][]string)
	var orderedKeys []string
	var fnameKey = "filename"
	orderedKeys = append(orderedKeys, fnameKey)
	for _, k := range files {
		table[fnameKey] = append(table[fnameKey], k)
		for _, value := range data[k] {
			_, isKeyInMap := table[value.Name]
			if index == 0 { // the first time simply add things
				table[value.Name] = append(
					table[value.Name], value.Value)
			} else if isKeyInMap { // if the things exist, add them in the right place
				// if we're in the right place to append, append
				if len(table[value.Name]) == index {
					table[value.Name] = append(
						table[value.Name], value.Value)
				} else { // if we have skipped some, pad then append
					for i := len(table[value.Name]); i <= index; i++ {
						table[value.Name] = append(table[value.Name], "")
					}
					table[value.Name] = append(
						table[value.Name], value.Value)
				}
			} else { // if the things don't exist, pad them then add them
				for i := 0; i <= index; i++ {
					table[value.Name] = append(
						table[value.Name], "")
				}
				table[value.Name] = append(
					table[value.Name], value.Value)
			}
			if !isKeyInMap {
				orderedKeys = append(orderedKeys, value.Name)
			}
		}
		index++
	}

	// convert all that to an array of arrays
	var headers []string
	var retTable [][]string
	for _, key := range orderedKeys {
		headers = append(headers, key)
	}
	retTable = append(retTable, headers)
	for i := 0; i < len(table[fnameKey]); i++ {
		var row []string
		for _, key := range orderedKeys {
			row = append(row, table[key][i])
		}
		retTable = append(retTable, row)
	}

	return retTable
}

func readPdfs(source string) (map[string][]Fields, []string) {
	// has side-effects.  not good
	var fileNames []string
	var fileFields = make(map[string][]Fields)

	dir, err := os.Open(source)
	if err != nil {
		log.Fatalf("Couldn't open \"%s\"\n", source)
	}
	defer dir.Close()

	files, err := dir.Readdir(0)
	if err != nil {
		log.Fatal(err)
	}

	for _, file := range files {

		if !strings.HasSuffix(file.Name(), ".pdf") {
			log.Debugf("Skipping file: %s\n", file.Name())
			continue
		}

		log.Debugf("Reading file: %s\n", file.Name())
		path := fmt.Sprintf("%s/%s", source, file.Name())
		fields, err := parsePdfForm(path)
		if err != nil {
			for _, e := range err {
				log.Warnf("%v\n", e)
			}
		} else {
			fileFields[file.Name()] = fields
			fileNames = append(fileNames, file.Name())
		}
	}
	sort.Strings(fileNames)
	return fileFields, fileNames
}

func parsePdfForm(source string) ([]Fields, []string) {
	var ff []Fields
	var errs []string

	// For some reason the PDF parser puts some values inside a
	// set of parentheses; this matches those so we can remove
	// them later
	re := regexp.MustCompile(`\(|\)`)

	ctx, err := api.ReadContextFile(source)
	if err != nil {
		errs = append(errs,
			fmt.Sprintf("Couldn't read file %s", source))
	}

	cat, err := ctx.Catalog()
	if err != nil {
		errs = append(errs,
			fmt.Sprintf("Couldn't process file %s", source))
	}

	acroform, ok := cat.Find("AcroForm")
	if !ok {
		errs = append(errs,
			fmt.Sprintf("No form found in file %s", source))
	}

	adict, err := ctx.DereferenceDict(acroform)
	if err != nil {
		errs = append(errs,
			fmt.Sprintf("Form data could not be read from %s", source))
	}

	if errs != nil {
		return nil, errs
	}

	fields := adict.ArrayEntry("Fields")

	for i, o := range fields {
		ir := o.(pdfcpu.IndirectRef)
		e, ok := ctx.FindTableEntryForIndRef(&ir)
		if !ok {
			errs = append(errs,
				fmt.Sprintf("Form data seems corrupt in %s (no XrefTable entry for %v)",
					source, ir))
			//log.Fatalf("No XrefTableEntry for %v", ir)
		}

		d, ok := e.Object.(pdfcpu.Dict)
		if !ok {
			errs = append(errs,
				fmt.Sprintf("Form data seems corrupt in %s (Object %v isn't a dictionary)",
					source, ir))
			//log.Fatalf("Object %v is not a Dict", ir)
		}

		v := d.StringEntry("T")
		if v == nil {
			errs = append(errs,
				fmt.Sprintf("Form data seems corrupt in %s (no name for form field #%v)",
					source, i))
		}

		fieldName := *v

		// v = d.NameEntry("FT")
		// if v == nil {
		// 	errs = append(errs, "Form data seems corrupt (found a form field with no type)")
		// 	//log.Println("No field type for field %v %v", i, fieldName)
		// }

		// fieldType := *v

		fieldValue, _ := d.Find("V")
		fV := fmt.Sprintf("%v", fieldValue)
		// This removes the parentheses mentioned above by
		// replacing them with nothing
		fV = re.ReplaceAllLiteralString(fV, "")

		ff = append(ff, Fields{fieldName, fV})
	}
	return ff, errs
}

func getDirFromDialog() string {
	_, err := dlgs.Info("Welcome", "Welcome to the PDF Form Data to CSV file Tool")
	if err != nil {
		log.Fatal(err)
	}
	source, success, err := dlgs.File("Directory with PDF files", "", true)
	if err != nil {
		log.Fatal(err)
	}
	if !success {
		dlgs.Info("No selection", "You didn't chose anything... 🤷🏼‍♂️")
		return ""
	}
	return source
}

func initLogfile() {
	logFileName := fmt.Sprintf(
		"%s/pdfform2csv.log",
		os.TempDir())

	logFile, err := os.OpenFile(
		logFileName,
		os.O_CREATE|os.O_WRONLY,
		0666)
	if err == nil {
		log.SetOutput(logFile)
	}
}
