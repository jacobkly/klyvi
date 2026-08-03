package rankings

// TMDB genre id → name tables. These are fixed reference data from TMDB's
// /genre/movie/list and /genre/tv/list endpoints; they change rarely enough
// to embed rather than fetch. The list endpoints return only genre ids, so
// this is how a ranking gets human-readable genres without a per-item detail
// call.

var movieGenres = map[int]string{
	28: "Action", 12: "Adventure", 16: "Animation", 35: "Comedy",
	80: "Crime", 99: "Documentary", 18: "Drama", 10751: "Family",
	14: "Fantasy", 36: "History", 27: "Horror", 10402: "Music",
	9648: "Mystery", 10749: "Romance", 878: "Science Fiction",
	10770: "TV Movie", 53: "Thriller", 10752: "War", 37: "Western",
}

var tvGenres = map[int]string{
	10759: "Action & Adventure", 16: "Animation", 35: "Comedy",
	80: "Crime", 99: "Documentary", 18: "Drama", 10751: "Family",
	10762: "Kids", 9648: "Mystery", 10763: "News", 10764: "Reality",
	10765: "Sci-Fi & Fantasy", 10766: "Soap", 10767: "Talk",
	10768: "War & Politics", 37: "Western",
}
