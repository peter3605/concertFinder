import SwiftUI

/// The first launch after signing in.
///
/// A cold 200-artist profile measured roughly 250s of MusicBrainz plus 86s of
/// Nominatim against a 300s scan budget. That is *minutes* of empty state on a
/// device someone is holding, and plan §5.5 is blunt about it: a spinner will
/// not survive contact with a real user, and this is the single most likely
/// reason a TestFlight user does not come back on day two.
///
/// So this screen does four things a spinner cannot:
///
/// 1. **Asks where they are, before anything starts.** Login no longer
///    pre-warms a scan for a user with no location of their own, and the feed
///    read is what enqueues one — so this question is the gate on the whole
///    flow. Getting it wrong is not a cosmetic cost: a scan against the
///    deployment's fallback city is five minutes and a chunk of a 250-call
///    daily allowance, and every bit of it is discarded the moment the user
///    names their own city, which produces a fresh `location_key` with no
///    snapshot and a second full scan.
/// 2. **Says what is happening**, in terms of the user's own library rather
///    than ours — "reading your library", not "hydrating affinity sources".
/// 3. **Offers real work to do while waiting.** Following artists is
///    something the user would otherwise be prompted for later, and it
///    improves the alerts they get afterwards.
/// 4. **Renders results.** Partial results from their own scan as they land,
///    and — behind that, from the first moment — what is simply on sale
///    nearby, from `/api/discover`. The screen dissolves into the feed
///    instead of switching to it.
struct FirstRunView: View {
    @Environment(FeedModel.self) private var model
    @Environment(ArtistsModel.self) private var artists
    @Environment(DiscoverModel.self) private var discover

    /// Drives the copy, not a progress bar. There is no honest percentage to
    /// show — the scan's duration depends on the user's library size and how
    /// much of it is already cached — so this narrates stages instead of
    /// pretending to measure.
    @State private var elapsed: TimeInterval = 0
    @State private var showingLocation = false
    @State private var showingArtists = false

    private let tick = Timer.publish(every: 1, on: .main, in: .common).autoconnect()

    var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: Metrics.loose) {
                if model.isAwaitingLocation {
                    locationStep
                } else {
                    header
                    suggestions
                    foundSoFar
                }

                nearby

                SpotifyAttribution()
                    .frame(maxWidth: .infinity, alignment: .center)
            }
            .padding(Metrics.gutter)
        }
        .background(Color.screenBackground)
        .navigationTitle(model.isAwaitingLocation ? "Welcome" : "Setting up")
        .navigationBarTitleDisplayMode(.inline)
        .onReceive(tick) { _ in elapsed += 1 }
        // Keyed on the origin so answering the location question reloads the
        // backdrop for the city the user actually named.
        .task(id: discoverKey) {
            guard let origin = model.searchOrigin else { return }
            await discover.load(
                latitude: origin.latitude,
                longitude: origin.longitude,
                radiusMiles: origin.radiusMiles
            )
        }
        .sheet(isPresented: $showingLocation) {
            NavigationStack {
                LocationView()
                    .toolbar {
                        ToolbarItem(placement: .topBarTrailing) {
                            Button("Done") { showingLocation = false }
                        }
                    }
            }
        }
        .sheet(isPresented: $showingArtists) {
            NavigationStack {
                ArtistsView()
                    .toolbar {
                        ToolbarItem(placement: .topBarTrailing) {
                            Button("Done") { showingArtists = false }
                        }
                    }
            }
        }
    }

    /// Step one, and on a genuine first run the only thing on screen above
    /// the backdrop. Nothing is fetched until it is answered.
    private var locationStep: some View {
        VStack(alignment: .leading, spacing: Metrics.gutter) {
            VStack(alignment: .leading, spacing: Metrics.tight) {
                Text("Where are you?")
                    .font(.title2.weight(.semibold))
                Text("We search ticket listings around one place. Tell us where before we start and the first search is the one you wanted — otherwise we spend it on a default city and have to run the whole thing again.")
                    .font(.subheadline)
                    .foregroundStyle(.secondary)
                    .fixedSize(horizontal: false, vertical: true)
            }

            Button {
                showingLocation = true
            } label: {
                Label("Set your location", systemImage: "location")
                    .frame(maxWidth: .infinity)
            }
            .buttonStyle(.borderedProminent)
            .controlSize(.large)

            // Not a dead end. Someone who declines location access and does
            // not know what to type still has to be able to get into the app.
            Button("Search near a default city instead") {
                Task { await model.continueWithoutLocation() }
            }
            .font(.footnote)
            .frame(maxWidth: .infinity)
        }
        .padding(Metrics.gutter)
        .background(Color.cardBackground)
        .clipShape(RoundedRectangle(cornerRadius: Metrics.cardRadius, style: .continuous))
    }

    /// Partial results from the user's own scan. The whole point of the
    /// screen: the wait stops being empty the moment anything lands.
    @ViewBuilder
    private var foundSoFar: some View {
        if !model.events.isEmpty {
            VStack(alignment: .leading, spacing: Metrics.cardSpacing) {
                Text("Found so far")
                    .font(.headline)
                ForEach(model.events.prefix(5)) { event in
                    NavigationLink(value: event) {
                        EventCard(
                            event: event,
                            onToggleSave: { act in Task { await model.toggleSave(act: act) } },
                            onToggleSubscribe: { act in Task { await model.toggleSubscribe(act: act) } }
                        )
                    }
                    .buttonStyle(.plain)
                }
            }
        }
    }

    /// The signed-out backdrop: whatever is on sale nearby, from
    /// `/api/discover`.
    ///
    /// Two rules, both easy to break by accident. The label must never imply
    /// personalisation — this response is built from other users' cached
    /// scans around a coordinate and knows nothing about who is asking, so
    /// "popular shows near you", never "your" anything, and the subtitle says
    /// so outright. And nothing renders when there is nothing: no empty
    /// state, no error. A stranger's first screen is not the place to report
    /// that a cache we asked about happened to be cold.
    @ViewBuilder
    private var nearby: some View {
        if !discover.events.isEmpty {
            VStack(alignment: .leading, spacing: Metrics.cardSpacing) {
                VStack(alignment: .leading, spacing: 2) {
                    Text("Popular shows near you")
                        .font(.headline)
                    Text("Not from your library — just what's on sale nearby while we build your feed.")
                        .font(.caption)
                        .foregroundStyle(.secondary)
                        .fixedSize(horizontal: false, vertical: true)
                }
                // No save, no subscribe, and no tap through to a detail
                // screen: these acts carry no artist id and no saved state,
                // so every one of those controls would be inert.
                ForEach(discover.events) { event in
                    EventCard(event: event)
                }
            }
        }
    }

    private var discoverKey: String? {
        guard let origin = model.searchOrigin else { return nil }
        return "\(origin.latitude),\(origin.longitude),\(origin.radiusMiles)"
    }

    private var header: some View {
        VStack(alignment: .leading, spacing: Metrics.tight) {
            HStack(spacing: Metrics.tight) {
                ProgressView()
                    .controlSize(.small)
                Text(stage.title)
                    .font(.headline)
            }
            Text(stage.detail)
                .font(.subheadline)
                .foregroundStyle(.secondary)
                .fixedSize(horizontal: false, vertical: true)
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .padding(Metrics.gutter)
        .background(Color.cardBackground)
        .clipShape(RoundedRectangle(cornerRadius: Metrics.cardRadius, style: .continuous))
        // One announcement rather than a spinner plus two labels, and it
        // updates as the stage changes so VoiceOver users get the same
        // narration sighted users do.
        .accessibilityElement(children: .combine)
        .accessibilityLabel("\(stage.title). \(stage.detail)")
    }

    private var suggestions: some View {
        VStack(alignment: .leading, spacing: Metrics.tight) {
            Text("While you wait")
                .font(.headline)

            // Both of these make the scan that is currently running better,
            // which is why they are offered here rather than buried in
            // Settings for later.
            SuggestionRow(
                icon: "location",
                title: "Set your location",
                detail: model.isUsingFallbackLocation
                    ? "We're searching near a default city right now."
                    : "Change where we search, or widen the radius.",
                isUrgent: model.isUsingFallbackLocation
            ) { showingLocation = true }

            SuggestionRow(
                icon: "bell",
                title: "Follow artists",
                detail: artists.subscribed.isEmpty
                    ? "Get notified the moment they announce a show near you."
                    : "You're following \(artists.subscribed.count).",
                isUrgent: false
            ) { showingArtists = true }
        }
        .padding(Metrics.gutter)
        .background(Color.cardBackground)
        .clipShape(RoundedRectangle(cornerRadius: Metrics.cardRadius, style: .continuous))
    }

    /// Narrated stages. The thresholds are rough by design — they describe
    /// what the backend is doing in the order it does it, so the copy stays
    /// true even when the timing varies with library size.
    private var stage: (title: String, detail: String) {
        if !model.events.isEmpty {
            return ("Still looking",
                    "We've found \(model.events.count) so far and we're still working through your artists. New shows will appear here as we find them.")
        }
        switch elapsed {
        case ..<20:
            return ("Reading your library",
                    "We're working out which artists you actually listen to. Your listening history isn't stored — only the artist scores we derive from it.")
        case ..<75:
            return ("Searching for shows",
                    "Checking your artists against ticket listings near you. This takes a few minutes the first time, and it's much faster afterwards.")
        default:
            return ("Still searching",
                    "Smaller artists take longer — we look them up one at a time to stay within the limits those services set. You can close the app; we'll keep going and notify you.")
        }
    }
}

private struct SuggestionRow: View {
    let icon: String
    let title: String
    let detail: String
    let isUrgent: Bool
    var action: () -> Void

    /// The icon gutter is scaled rather than fixed, so it keeps pace with the
    /// text at accessibility sizes instead of clipping the glyph.
    @ScaledMetric(relativeTo: .subheadline) private var iconWidth: CGFloat = 24

    var body: some View {
        Button(action: action) {
            HStack(spacing: Metrics.tight) {
                Image(systemName: icon)
                    .foregroundStyle(isUrgent ? Color.accentColor : .secondary)
                    .frame(width: iconWidth)
                VStack(alignment: .leading, spacing: 2) {
                    Text(title)
                        .font(.subheadline.weight(.medium))
                    Text(detail)
                        .font(.caption)
                        .foregroundStyle(.secondary)
                        .fixedSize(horizontal: false, vertical: true)
                        .multilineTextAlignment(.leading)
                }
                Spacer(minLength: 0)
                Image(systemName: "chevron.right")
                    .font(.caption)
                    .foregroundStyle(.tertiary)
            }
            .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
        .padding(.vertical, 6)
        .accessibilityHint(detail)
    }
}
