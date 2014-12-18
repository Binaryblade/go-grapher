package main

import "golang.org/x/net/html"
import "golang.org/x/net/html/atom"
import "net/http"
import "log"
import "fmt"
import "io"
import "net/url"
import "sync"
import "runtime"
import "time"
import "os"

//takes a resource name, opens it and returns all valid resources within
type Scraper interface {
	Scrape(string) []string
}

type DomainScraper string

//creates a new domain scraper to look for all links in an environment
func NewDomainScraper(domainname string) DomainScraper {
	return DomainScraper(domainname)
}


func (d DomainScraper) String() string {
	return string(d)
}

func (d DomainScraper) Scrape(name string) []string {

	//default empty but preallocate room for some typical amount of links
	retval := make([]string,0,100)

	//build up the full web address
	toScrape,_ := d.filter(name)
	
	
	//go get the page
	resp, err := http.Get(toScrape)
	if err != nil {
		log.Print(err.Error())
		return retval
	}
	defer resp.Body.Close()

	//Scan the html document for links
	z := html.NewTokenizer(resp.Body)
	for {
		t := z.Next()
		if t == html.ErrorToken {
			switch z.Err() {
				case io.EOF:
				default:
					log.Print(z.Err())
			}
			break;
		}
		token := z.Token()
		//If item is and A tag check for href attributes
		if token.Type == html.StartTagToken && token.DataAtom == atom.A {
			for _,v := range token.Attr {
				if v.Key == "href" {
					//run it through a check to make sure it is the current domain
					mod, good := d.filter(v.Val)
					if good {
						retval = append(retval, mod) //append valid pages to the return list
					}
				}
			}
		}
	}
	return retval
}

//Returns true if name is in the same domain as the scraper
func (d DomainScraper) filter(name string) (string,bool) {
	url, err := url.Parse(name)
	if err != nil {
		return "", false
	}
	domain, _ := url.Parse(string(d))
	domain.Host = string(d)
	domain.Scheme = "http"
	abs := domain.ResolveReference(url)
	return abs.String(), domain.Host == abs.Host
}

//holder to return the results of the page scan
type response struct {
	site string
	links []string
}

//Crawler object to coordinate the scan
type Crawler struct {
	Results map[string][]string //result set of connection
	Scraper DomainScraper	//scrapper object that does the work of scanning each page
	Concurrency int
}

func NewCrawler(site string,count int) *Crawler {
	crawl := Crawler{
		Scraper: NewDomainScraper(site),
		Results: make(map[string][]string),
		Concurrency: count}
	return &crawl
}


func (c *Crawler) addResponse(r response) []string {
	output := make([]string,0)
	 
	//use a map as a uniqueness filter
	unique_filter := make(map[string]bool)
	for _,v := range r.links {
		unique_filter[v]=true
	}

	//extract unique results from map
	for k := range unique_filter {
		output = append(output, k)
	}

	//Store results of this scan
	c.Results[r.site]=output

	retval := make([]string,0)
	//filter for results not already scanned
	for _,v := range output {
		if _,ok := c.Results[v]; !ok {
			if d,ok := c.Scraper.filter(v); ok {
				retval = append(retval,d)
			}
		}
	}
	
	return retval
}

//Scan the domain
func (c *Crawler) Crawl() {

	var pr sync.WaitGroup
	var workers sync.WaitGroup

	//Fill a large enough buffer to hold pending responses
	reqChan := make(chan string,100e6)

	//channel to funnel results
	respChan := make(chan response)

	pr.Add(1) //Add the base site to the pending responses
	reqChan<-"http://"+c.Scraper.String() //Push the pending request
	
	//Spin off a closer, will return when wait group is empty
	go func() { pr.Wait(); close(reqChan) } ()

	
	//Spin up a bunch of simultaneous parsers and readers
	workers.Add(c.Concurrency)
	for i:=0; i<c.Concurrency;i++ {
		go func() {
			defer workers.Done()
			for {
				//pull requests and close if no more
				t,ok := <-reqChan
				if !ok {
					return 
				}
				//report results
				respChan <-response{site: t, links: c.Scraper.Scrape(t)}
			}
		}()
	}

	//when the workers are finished kill the response channel
	go func() { workers.Wait(); close(respChan)} ()

	
	//Spin up a quick logger that reports the queue length
	durationTick := time.Tick(time.Second)
	go func() { 
		for range durationTick {
				log.Printf("Currently %d items in queue\n",len(reqChan))
		}
	} ()

	//Actually deal with the data coming back from the scrapers
	for {
		response,ok := <-respChan // pull reponses
		if !ok {
			break
		}
		
		//push the collected links and get the unique ones back
		subset := c.addResponse(response)
	
		//Queue up a job to scan unique reponses which came back
		for _,v := range subset {
			pr.Add(1) //insert all the new requests and increment pending
			reqChan <-v
		}
		pr.Done() //Finish serviceing this request
	}

	c.compressResults()
}

func (c *Crawler) compressResults() {
	for k,v := range c.Results {
		c.Results[k] = c.filterLinks(v)
	}
}

func (c *Crawler) filterLinks(links []string) []string {
	retval := make([]string,0)
	for _,v := range links {
		if _,ok := c.Results[v]; ok {
			retval = append(retval,v)
		}
	}
	return retval
}


//Implement the Stringer interface
//default string output is dot file format
func (c *Crawler) String() string {
	retval := fmt.Sprintf("digraph Scraped {\n")
	nodeLookup := make(map[string]string)
	count := 0
	for k := range c.Results {
		nodeLookup[k] = fmt.Sprintf("N%d",count)
		count++
		//output node names here
	}

	for k,v := range c.Results {
		source := nodeLookup[k]
		for _,out := range v {
			dest, ok := nodeLookup[out]
			if ok {
				retval += fmt.Sprintf("\t%s -> %s; \n",source,dest)
			}
		}
	}
	retval += "}\n"
	return retval
}



func main() {
	//lets make sure we have a few actual threads available
	runtime.GOMAXPROCS(8)
	if len(os.Args) != 2 {
		fmt.Println("Usage is: grapher basehost")
		fmt.Println("grapher builds a link graph of a host domain")
		return;
	}

	//Build a Crawler unit
	d := NewCrawler(os.Args[1],100)

	//begin crawl at domain root
	d.Crawl()

	//pretty print results in dot file format
	fmt.Printf("%v",d)
}
