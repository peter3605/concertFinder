import Foundation
import Observation

/// The account's hand-picked artists and genres (CF-30, CF-31): what a feed
/// is built from for an account with no Spotify library, and an addition to
/// one for an account with one.
///
/// One instance for the app, held by AppContainer, because onboarding and
/// Settings edit the same lists and must agree about them -- a pick added in
/// one place and invisible in the other is the kind of disagreement nobody
/// reports, they just add it twice and hit the cap.
@MainActor
@Observable
final class PicksModel {
    private(set) var artists: [ArtistPick] = []
    private(set) var genres: [GenrePick] = []
    /// The server's caps, from the list responses. The defaults are only
    /// there until the first load answers; the server enforces the real ones.
    private(set) var maxArtists = 30
    private(set) var maxGenres = 5
    /// Top-level Ticketmaster music genres. Loaded once per session: the
    /// catalog is the same for everyone and changes rarely.
    private(set) var catalog: [TMGenre] = []
    private(set) var results: [PickSearchResult] = []
    private(set) var isSearching = false
    private(set) var isLoaded = false
    /// An attraction or genre ID with a request in flight, so its control
    /// can show that and refuse a second tap.
    private(set) var busyIDs: Set<String> = []
    private(set) var error: APIError?

    var query = "" {
        didSet {
            guard query != oldValue else { return }
            scheduleSearch()
        }
    }

    private let api: APIClient
    private var searchTask: Task<Void, Never>?

    /// Every search not already in the server's 24h cache spends one of the
    /// caller's daily Ticketmaster calls, so keystrokes are collapsed into
    /// one request the way ArtistsModel's are.
    private static let debounce: Duration = .milliseconds(400)

    init(api: APIClient) {
        self.api = api
    }

    var total: Int { artists.count + genres.count }

    func isPicked(_ result: PickSearchResult) -> Bool {
        artists.contains { $0.attractionId == result.attractionId }
    }

    func isPicked(_ genre: TMGenre) -> Bool {
        genres.contains { $0.genreId == genre.id }
    }

    var artistsFull: Bool { artists.count >= maxArtists }
    var genresFull: Bool { genres.count >= maxGenres }

    func load() async {
        do {
            async let a = api.artistPicks()
            async let g = api.genrePicks()
            let (artistResponse, genreResponse) = try await (a, g)
            artists = artistResponse.picks ?? []
            maxArtists = artistResponse.max
            genres = genreResponse.picks ?? []
            maxGenres = genreResponse.max
            isLoaded = true
            error = nil
        } catch is CancellationError {
        } catch let apiError as APIError {
            error = apiError
        } catch {
            self.error = .unknown(error.localizedDescription)
        }
        if catalog.isEmpty {
            await loadCatalog()
        }
    }

    func loadCatalog() async {
        do {
            catalog = try await api.availableGenres()
        } catch is CancellationError {
        } catch let apiError as APIError {
            // Not fatal to the screen: artist picks still work without it.
            error = apiError
        } catch {
            self.error = .unknown(error.localizedDescription)
        }
    }

    // MARK: - Artists

    func add(_ result: PickSearchResult) async {
        guard !isPicked(result), !artistsFull, !busyIDs.contains(result.attractionId) else { return }
        busyIDs.insert(result.attractionId)
        defer { busyIDs.remove(result.attractionId) }
        do {
            // The server's copy, not the search row: it names the pick from
            // Ticketmaster, because the name is part of the shared dedup key.
            let pick = try await api.addPick(attractionID: result.attractionId)
            if !artists.contains(where: { $0.attractionId == pick.attractionId }) {
                artists.append(pick)
            }
            error = nil
        } catch let apiError as APIError {
            error = apiError
        } catch {
            self.error = .unknown(error.localizedDescription)
        }
    }

    func remove(_ pick: ArtistPick) async {
        guard !busyIDs.contains(pick.attractionId) else { return }
        busyIDs.insert(pick.attractionId)
        defer { busyIDs.remove(pick.attractionId) }
        do {
            try await api.removePick(attractionID: pick.attractionId)
            artists.removeAll { $0.attractionId == pick.attractionId }
            error = nil
        } catch let apiError as APIError {
            error = apiError
        } catch {
            self.error = .unknown(error.localizedDescription)
        }
    }

    // MARK: - Genres

    /// Not optimistic, unlike the bell: the server may refuse at the cap or
    /// for a genre that is no longer top-level, and a chip that lit and then
    /// went dark reads as a glitch rather than as an answer.
    func toggle(_ genre: TMGenre) async {
        guard !busyIDs.contains(genre.id) else { return }
        busyIDs.insert(genre.id)
        defer { busyIDs.remove(genre.id) }
        do {
            if isPicked(genre) {
                try await api.removeGenre(genreID: genre.id)
                genres.removeAll { $0.genreId == genre.id }
            } else {
                guard !genresFull else { return }
                let pick = try await api.addGenre(genreID: genre.id)
                genres.append(pick)
            }
            error = nil
        } catch let apiError as APIError {
            error = apiError
        } catch {
            self.error = .unknown(error.localizedDescription)
        }
    }

    // MARK: - Search

    private func scheduleSearch() {
        searchTask?.cancel()
        let term = query.trimmingCharacters(in: .whitespacesAndNewlines)
        guard term.count >= 2 else {
            results = []
            isSearching = false
            return
        }
        searchTask = Task { [weak self] in
            try? await Task.sleep(for: Self.debounce)
            guard !Task.isCancelled, let self else { return }
            await self.runSearch(term)
        }
    }

    private func runSearch(_ term: String) async {
        isSearching = true
        do {
            let found = try await api.searchPicks(query: term)
            // A slower answer to an older query must not replace a newer one.
            if query.trimmingCharacters(in: .whitespacesAndNewlines) == term {
                results = found
            }
            error = nil
        } catch is CancellationError {
        } catch let apiError as APIError {
            error = apiError
        } catch {
            self.error = .unknown(error.localizedDescription)
        }
        isSearching = false
    }

    func clearError() { error = nil }

    /// Sign-out: nothing of the previous account's may survive.
    func reset() {
        searchTask?.cancel()
        searchTask = nil
        query = ""
        artists = []
        genres = []
        results = []
        catalog = []
        busyIDs = []
        isSearching = false
        isLoaded = false
        error = nil
    }
}
