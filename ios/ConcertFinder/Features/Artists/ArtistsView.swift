import Observation
import SwiftUI

@MainActor
@Observable
final class ArtistsModel {
    private(set) var subscribed: [SubscribedArtist] = []
    private(set) var results: [SubscribedArtist] = []
    private(set) var isSearching = false
    private(set) var error: APIError?

    var query = "" {
        didSet {
            guard query != oldValue else { return }
            scheduleSearch()
        }
    }

    private let api: APIClient
    private var searchTask: Task<Void, Never>?

    /// Every keystroke would otherwise be a server-side artist lookup, and
    /// each one spends the user's own upstream quota (plan §6). 300ms is long
    /// enough to collapse a typed word into one request.
    private static let debounce: Duration = .milliseconds(300)

    init(api: APIClient) {
        self.api = api
    }

    private var subscribedIDs: Set<String> {
        Set(subscribed.map(\.id))
    }

    func isSubscribed(_ artist: SubscribedArtist) -> Bool {
        subscribedIDs.contains(artist.id)
    }

    func load() async {
        do {
            subscribed = try await api.subscribedArtists()
            error = nil
        } catch let apiError as APIError {
            error = apiError
        } catch {
            self.error = .unknown(error.localizedDescription)
        }
    }

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
            results = try await api.searchArtists(query: term)
            error = nil
        } catch is CancellationError {
            // Superseded by a newer keystroke.
        } catch let apiError as APIError {
            error = apiError
        } catch {
            self.error = .unknown(error.localizedDescription)
        }
        isSearching = false
    }

    func toggle(_ artist: SubscribedArtist) async {
        let wasSubscribed = isSubscribed(artist)
        // Optimistic, so the row responds to the tap immediately.
        if wasSubscribed {
            subscribed.removeAll { $0.id == artist.id }
        } else {
            subscribed.append(artist)
        }
        do {
            if wasSubscribed {
                try await api.unsubscribe(artistID: artist.id)
            } else {
                try await api.subscribe(artistID: artist.id)
            }
        } catch {
            // Put it back — a control that lies about what it did is worse
            // than a slow one.
            if wasSubscribed {
                subscribed.append(artist)
            } else {
                subscribed.removeAll { $0.id == artist.id }
            }
            self.error = error as? APIError ?? .unknown(error.localizedDescription)
        }
    }

    /// Sign-out. The debounce task goes too: a keystroke in flight would
    /// otherwise land a search against a session that no longer exists.
    func reset() {
        searchTask?.cancel()
        searchTask = nil
        query = ""
        subscribed = []
        results = []
        isSearching = false
        error = nil
    }
}

struct ArtistsView: View {
    @Environment(ArtistsModel.self) private var model

    var body: some View {
        @Bindable var model = model

        NavigationStack {
            List {
                // The model has always set `error` here and the screen has
                // never shown it: a failed load left "You're not getting
                // alerts for any artists yet", which reads as the user having
                // no subscriptions rather than as an outage.
                if let error = model.error {
                    Section {
                        InfoBanner(kind: .error(error.userMessage))
                            .listRowInsets(EdgeInsets())
                            .listRowBackground(Color.clear)
                        Button("Try again") { Task { await model.load() } }
                    }
                }

                if !model.query.isEmpty {
                    Section("Results") {
                        if model.isSearching && model.results.isEmpty {
                            HStack { ProgressView(); Text("Searching…").foregroundStyle(.secondary) }
                        } else if model.results.isEmpty {
                            Text("No artists found.").foregroundStyle(.secondary)
                        } else {
                            ForEach(model.results) { artist in
                                ArtistRow(artist: artist, isSubscribed: model.isSubscribed(artist)) {
                                    Task { await model.toggle(artist) }
                                }
                            }
                        }
                    }
                }

                Section {
                    if model.subscribed.isEmpty && model.error != nil {
                        Text("We couldn't load your artists.")
                            .foregroundStyle(.secondary)
                    } else if model.subscribed.isEmpty {
                        Text("You're not getting alerts for any artists yet. Search above to add one.")
                            .foregroundStyle(.secondary)
                    } else {
                        ForEach(model.subscribed) { artist in
                            ArtistRow(artist: artist, isSubscribed: true) {
                                Task { await model.toggle(artist) }
                            }
                        }
                    }
                } header: {
                    Text("Getting alerts")
                } footer: {
                    VStack(alignment: .leading, spacing: Metrics.tight) {
                        Text("We'll notify you as soon as one of these artists announces a show near you.")
                        SpotifyAttribution()
                    }
                }
            }
            .navigationTitle("Artists")
            .searchable(text: $model.query, prompt: "Search artists")
            .task { await model.load() }
            .refreshable { await model.load() }
        }
    }
}

private struct ArtistRow: View {
    let artist: SubscribedArtist
    let isSubscribed: Bool
    var onToggle: () -> Void

    var body: some View {
        HStack {
            VStack(alignment: .leading, spacing: 2) {
                Text(artist.name)
                if let genres = artist.genres, !genres.isEmpty {
                    Text(genres.prefix(2).joined(separator: " · "))
                        .font(.caption)
                        .foregroundStyle(.secondary)
                }
            }
            Spacer()
            Button(action: onToggle) {
                Image(systemName: isSubscribed ? "bell.fill" : "bell")
            }
            .buttonStyle(.plain)
            .foregroundStyle(isSubscribed ? Color.accentColor : Color.secondary)
            .accessibilityLabel(isSubscribed
                                ? "Stop alerts for \(artist.name)"
                                : "Get alerts for \(artist.name)")
        }
    }
}
