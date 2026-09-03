import SwiftUI

struct FeedView: View {
    @Environment(FeedModel.self) private var model
    @Environment(AppContainer.self) private var container
    @Environment(AuthController.self) private var auth
    @Environment(PushRegistrar.self) private var push
    @Environment(\.scenePhase) private var scenePhase
    @State private var showingFilters = false
    /// Read once, at construction: a hint the user dismissed must not come
    /// back when the view is rebuilt.
    @State private var showsIntroHint = !HintStore.isDismissed(.saveVersusSubscribe)

    var body: some View {
        @Bindable var model = model

        NavigationStack(path: $model.path) {
            Group {
                // A genuine cold start is minutes of work, not a spinner's
                // worth. See FirstRunView and plan §5.5.
                if model.isFirstRun {
                    FirstRunView()
                } else if model.isLoading && model.events.isEmpty {
                    ProgressView("Finding concerts near you…")
                        .frame(maxWidth: .infinity, maxHeight: .infinity)
                } else if model.isEmpty {
                    emptyState
                } else {
                    list
                }
            }
            // On the Group, not on `list`. Attached to the list they covered
            // only the branch that renders it, so FirstRunView's
            // NavigationLink(value:) had no destination and its cards were
            // dead — a tap did nothing but log a purple runtime warning. The
            // path-driven push from a notification needs the same coverage.
            .navigationDestination(for: Event.self) { event in
                EventDetailView(event: event)
            }
            .navigationDestination(for: FeedRoute.self) { route in
                switch route {
                case .location: LocationView()
                }
            }
            .background(Color.screenBackground)
            .navigationTitle("Concerts")
            .toolbar { toolbar }
            // A pull re-reads the snapshot, which is free. The rescan that
            // spends the user's daily Ticketmaster allowance is a deliberate
            // tap in the toolbar — a gesture people make by accident should
            // not cost quota, and the 429 it earned was never shown.
            .refreshable { await model.load() }
            .sheet(isPresented: $showingFilters) {
                FiltersSheet(filters: $model.filters, facets: model.facets)
            }
            .task { await model.start() }
            .onChange(of: scenePhase) { _, phase in
                // A 10-second timer that survives backgrounding is a battery
                // complaint and an App Review question.
                switch phase {
                case .active: model.resumePollingIfNeeded()
                case .background, .inactive: model.suspendPolling()
                @unknown default: break
                }
            }
        }
        // Outside the NavigationStack, and deliberately not stacked beside
        // the filters sheet: two `.sheet(isPresented:)` on one view is a
        // presentation conflict waiting for the day both are true.
        //
        // Raised by the model the first time a scan settles with results in
        // it. Nothing here decides *when* — that rule is
        // FeedModel.shouldOfferPushPrompt, where it can be tested.
        .sheet(isPresented: $model.isShowingPushPrompt) {
            PushPrimerView(onAccept: enablePush)
        }
    }

    private var list: some View {
        ScrollView {
            LazyVStack(alignment: .leading, spacing: Metrics.cardSpacing, pinnedViews: [.sectionHeaders]) {
                banners

                ForEach(model.sections, id: \.key) { section in
                    Section {
                        ForEach(section.events) { event in
                            NavigationLink(value: event) {
                                EventCard(
                                    event: event,
                                    onToggleSave: { act in Task { await model.toggleSave(act: act) } },
                                    onToggleSubscribe: { act in Task { await model.toggleSubscribe(act: act) } }
                                )
                            }
                            .buttonStyle(.plain)
                        }
                    } header: {
                        Text(section.heading)
                            .font(.subheadline.weight(.semibold))
                            .frame(maxWidth: .infinity, alignment: .leading)
                            .padding(.vertical, 6)
                            .background(Color.screenBackground)
                    }
                }

                SpotifyAttribution()
                    .frame(maxWidth: .infinity, alignment: .center)
                    .padding(.top, Metrics.loose)
            }
            .padding(Metrics.gutter)
        }
    }

    /// Introduces the bookmark and the bell as a pair.
    ///
    /// Both controls are individually legible and nothing said what the pair
    /// was *for*: the bell is the retention action — it is about shows that
    /// do not exist yet — and next to a bookmark it reads as a second way to
    /// save. One sentence, once, dismissible for good.
    @ViewBuilder
    private var introHint: some View {
        if showsIntroHint {
            HStack(alignment: .top, spacing: Metrics.tight) {
                VStack(alignment: .leading, spacing: 2) {
                    Text("Two things you can do here")
                        .font(.footnote.weight(.semibold))
                    Text("Tap the bookmark to keep a show in Saved, or the bell to get alerted whenever that artist announces a new one near you.")
                        .font(.caption)
                        .foregroundStyle(.secondary)
                        .fixedSize(horizontal: false, vertical: true)
                }
                Spacer(minLength: 0)
                Button {
                    showsIntroHint = false
                    HintStore.dismiss(.saveVersusSubscribe)
                } label: {
                    Image(systemName: "xmark")
                        .font(.caption)
                        .foregroundStyle(.secondary)
                }
                .buttonStyle(.plain)
                .accessibilityLabel("Dismiss")
            }
            .padding(Metrics.gutter)
            .background(Color(.tertiarySystemFill))
            .clipShape(RoundedRectangle(cornerRadius: Metrics.cardRadius, style: .continuous))
        }
    }

    @ViewBuilder
    private var banners: some View {
        introHint
        // Four models set `error` and this was the screen that rendered none
        // of it: a 500 or a decode failure left an empty list under "Nothing
        // coming up near you yet", which blames the user's city for our
        // outage. Suppressed only when the offline banner below already says
        // it, so the two do not stack.
        if let error = model.error, !model.isShowingCachedData {
            InfoBanner(kind: .error(error.userMessage))
        }
        if let refusal = model.rescanRefusal {
            InfoBanner(kind: .throttled(reason: refusal.reason, until: refusal.until))
        }
        if model.missingDeepLinkEvent {
            InfoBanner(kind: .missingEvent)
        }
        // `complete: false` is a UI state, not a log line — a quiet week and
        // a truncated scan are indistinguishable without saying so.
        if !model.complete {
            if let retryAfter = model.retryAfter {
                InfoBanner(kind: .quotaExhausted(until: retryAfter))
            } else {
                InfoBanner(kind: .incomplete)
            }
        }
        if model.isShowingCachedData {
            InfoBanner(kind: .offline(since: model.computedAt))
        }
        if model.isRefreshing {
            HStack(spacing: Metrics.tight) {
                ProgressView().controlSize(.small)
                Text("Checking for new shows…")
                    .font(.footnote)
                    .foregroundStyle(.secondary)
            }
            .frame(maxWidth: .infinity, alignment: .leading)
        }
        // A location the user never set is the deployment's fallback, so
        // prompt rather than silently showing someone else's city.
        if model.isUsingFallbackLocation {
            NavigationLink(value: FeedRoute.location) {
                HStack(spacing: Metrics.tight) {
                    Image(systemName: "location.slash")
                        .foregroundStyle(.secondary)
                    VStack(alignment: .leading, spacing: 2) {
                        Text("Set your location")
                            .font(.footnote.weight(.semibold))
                        Text("These are shows near a default city, not near you.")
                            .font(.caption)
                            .foregroundStyle(.secondary)
                    }
                    Spacer(minLength: 0)
                    Image(systemName: "chevron.right")
                        .font(.caption)
                        .foregroundStyle(.tertiary)
                }
                .padding(Metrics.gutter)
                .background(Color(.tertiarySystemFill))
                .clipShape(RoundedRectangle(cornerRadius: Metrics.cardRadius, style: .continuous))
            }
            .buttonStyle(.plain)
        }
    }

    /// An empty list has two very different causes and they were told the same
    /// story. "Nothing coming up near you yet" over a failed request is a lie
    /// about the user's city.
    private var emptyTitle: String {
        guard let error = model.error else { return "No concerts yet" }
        // A refused rescan did not fail to load anything — the list is empty
        // for whatever reason it already was.
        if case .throttled = error { return "No concerts yet" }
        return "We couldn't load your concerts"
    }

    private var hasLoadFailure: Bool {
        guard let error = model.error else { return false }
        if case .throttled = error { return false }
        return true
    }

    private var emptyState: some View {
        ContentUnavailableView {
            Label(emptyTitle, systemImage: hasLoadFailure ? "exclamationmark.triangle" : "music.mic")
        } description: {
            if let error = model.error {
                Text(error.userMessage)
            } else if !model.complete {
                Text("We couldn't check every artist in your profile. Search again, or widen your search radius.")
            } else if model.filters.activeCount > 0 {
                Text("No shows match your filters. Try clearing them.")
            } else {
                Text("Nothing coming up near you yet. We check daily and will notify you when something lands.")
            }
        } actions: {
            // ContentUnavailableView does not scroll, so "pull to refresh" was
            // advice the user could not follow from the only screen that
            // offered it. A button can be tapped.
            if hasLoadFailure {
                Button("Try again") { Task { await model.load() } }
            } else if !model.complete {
                Button("Search again") { Task { await model.requestRescan() } }
                    .disabled(!model.canRescan)
            } else if model.filters.activeCount == 0 {
                // The primary action on a genuinely quiet city, because it is
                // the most useful thing anyone can do from here: there is
                // nothing on sale to save, and the whole point of a
                // subscription is shows that do not exist yet. It was buried
                // on another tab. Not offered when a filter is hiding the
                // list — clearing that is the more useful action, and it is
                // right below.
                Button("Get alerts when an artist announces a show") {
                    container.selectedTab = .artists
                }
                .buttonStyle(.borderedProminent)
            }
            if model.filters.activeCount > 0 {
                Button("Clear filters") { model.filters = .empty }
            }
        }
    }

    /// The accepted branch of the soft prompt.
    ///
    /// The profile is updated alongside the server so Settings does not open
    /// showing the toggle off over a device that is registered — where the
    /// user's next tap would turn off the notifications they just asked for.
    @MainActor
    private func enablePush() async {
        guard await push.enable() else { return }
        if case .signedIn(var me) = auth.state {
            me.pushOptIn = true
            auth.updateProfile(me)
        }
    }

    @ToolbarContentBuilder
    private var toolbar: some ToolbarContent {
        ToolbarItem(placement: .topBarLeading) {
            if let computedAt = model.computedAt {
                Text("Updated \(computedAt.relativeDescription)")
                    .font(.caption2)
                    .foregroundStyle(.secondary)
            }
        }
        ToolbarItem(placement: .topBarTrailing) {
            Button {
                Task { await model.requestRescan() }
            } label: {
                if model.isRescanning {
                    ProgressView().controlSize(.small)
                } else {
                    Label("Search again", systemImage: "arrow.clockwise")
                }
            }
            .disabled(!model.canRescan)
            .accessibilityLabel("Search for new shows")
        }
        ToolbarItem(placement: .topBarTrailing) {
            Button {
                showingFilters = true
            } label: {
                Label("Filters", systemImage: model.filters.activeCount > 0
                      ? "line.3.horizontal.decrease.circle.fill"
                      : "line.3.horizontal.decrease.circle")
            }
            .accessibilityLabel(model.filters.activeCount > 0
                                ? "Filters, \(model.filters.activeCount) active"
                                : "Filters")
        }
    }
}

/// Non-event destinations reachable from the feed. Separate from Event so
/// navigationDestination can discriminate by type.
enum FeedRoute: Hashable {
    case location
}

/// The soft prompt in front of the system notification dialog.
///
/// It exists because the system dialog is a one-shot: iOS shows it once, a
/// decline is permanent from the app's side, and Settings is the only way
/// back. Asking on launch — before the user has seen a single concert — is
/// the prompt people decline, so this one is raised at the moment it has been
/// earned: the first scan finished and it found them shows.
///
/// Everything here is about being *declinable without cost*. "Not now" does
/// not call `requestAuthorization`, so the one system prompt is still
/// available from Settings later, and the copy says exactly what will be sent
/// rather than asking for permission in the abstract.
struct PushPrimerView: View {
    var onAccept: @MainActor () async -> Void

    @Environment(\.dismiss) private var dismiss
    @State private var isRequesting = false

    var body: some View {
        VStack(alignment: .leading, spacing: Metrics.loose) {
            VStack(alignment: .leading, spacing: Metrics.tight) {
                Image(systemName: "bell.badge")
                    .font(.largeTitle)
                    .foregroundStyle(Color.accentColor)
                    .accessibilityHidden(true)
                Text("Want to hear about new shows?")
                    .font(.title2.weight(.semibold))
                Text("Tickets for the artists you listen to go on sale and sell out between one launch of an app and the next. We'll send you a notification when an artist you follow announces a show near you — and nothing else.")
                    .font(.subheadline)
                    .foregroundStyle(.secondary)
                    .fixedSize(horizontal: false, vertical: true)
            }

            Spacer(minLength: 0)

            VStack(spacing: Metrics.tight) {
                Button {
                    isRequesting = true
                    Task {
                        await onAccept()
                        isRequesting = false
                        dismiss()
                    }
                } label: {
                    if isRequesting {
                        ProgressView().frame(maxWidth: .infinity)
                    } else {
                        Text("Turn on notifications").frame(maxWidth: .infinity)
                    }
                }
                .buttonStyle(.borderedProminent)
                .controlSize(.large)
                .disabled(isRequesting)

                Button("Not now") { dismiss() }
                    .disabled(isRequesting)

                Text("You can change this any time in Settings.")
                    .font(.caption)
                    .foregroundStyle(.secondary)
            }
            .frame(maxWidth: .infinity)
        }
        .padding(Metrics.loose)
        .background(Color.screenBackground)
        .presentationDetents([.medium])
    }
}
