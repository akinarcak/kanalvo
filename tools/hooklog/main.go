// hooklog, SRS'in gönderdiği sorguları ekrana yazar. Yalnızca hata ayıklama içindir.
package main

import (
	"flag"
	"io"
	"log"
	"net/http"
)

func main() {
	addr := flag.String("addr", ":8099", "dinlenecek adres")
	reject := flag.Bool("reject", false, "tüm sorguları reddet")
	flag.Parse()

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		log.Printf("%s %s %s", r.Method, r.URL.Path, body)
		if *reject {
			http.Error(w, `{"code":403}`, http.StatusForbidden)
			return
		}
		w.Write([]byte(`{"code":0}`))
	})
	log.Fatal(http.ListenAndServe(*addr, nil))
}
