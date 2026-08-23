import SwiftUI

/// The first launch after signing in.
///
/// `OnLoginSuccess` enqueues a pre-warm scan the moment the account is
/// created, but a cold 200-artist profile measured roughly 250s of MusicBrainz
/// plus 86s of Nominatim against a 300s scan budget. That is *minutes* of
/// empty state on a device someone is holding, and plan §5.5 is blunt about
/// it: a spinner will not survive contact with a real user, and this is the
/// single most likely reason a TestFlight user does not come back on day two.
///
/// So this screen does three things a spinner cannot:
///
/// 1. **Says what is happening**, in terms of the user's own library rather
///    than ours — "reading your library", not "hydrating affinity sources".
/// 2. **Offers real work to do while waiting.** Setting a location and
///    picking artists to follow are both things the user would otherwise be
///    prompted for later, and both *improve* the scan that is running.
/// 3. **Renders partial results as they land.** The SWR poll updates the feed
///    continuously, so shows appear underneath the progress rather than after
///    it. The screen dissolves into the feed instead of switching to it.
struct FirstRunView: View {
    @Environment(FeedModel.self) private var model
    @Environment(ArtistsModel.self) private var artists

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
                header
                suggestions

                if !model.events.isEmpty {
                    // Partial results. The whole point: the wait stops being
                    // empty the moment anything lands.
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

                SpotifyAttribution()
                    .frame(maxWidth: .infinity, alignment: .center)
            }
            .padding(Metrics.gutter)
        }
        .background(Color.screenBackground)
        .navigationTitle("Setting up")
        .navigationBarTitleDisplayMode(.inline)
        .onReceive(tick) { _ in elapsed += 1 }
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
