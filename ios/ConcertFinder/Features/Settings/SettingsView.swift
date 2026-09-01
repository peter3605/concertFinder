import SwiftUI

struct SettingsView: View {
    @Environment(AuthController.self) private var auth
    @Environment(ThemeStore.self) private var theme
    @Environment(PushRegistrar.self) private var push
    @Environment(\.openURL) private var openURL

    let api: APIClient
    let baseURL: URL

    @State private var digestOptIn = false
    @State private var instantOptIn = false
    @State private var pushOptIn = false
    @State private var showingDeleteConfirmation = false
    @State private var showingDisconnectConfirmation = false
    @State private var isDeleting = false
    @State private var isDisconnecting = false
    @State private var error: APIError?

    private var me: Me? {
        if case .signedIn(let user) = auth.state { return user }
        return nil
    }

    var body: some View {
        NavigationStack {
            Form {
                accountSection
                appearanceSection
                notificationsSection
                NavigationLink { LocationView() } label: {
                    Label("Location", systemImage: "location")
                }
                aboutSection
                connectionSection
                dangerSection
            }
            .navigationTitle("Settings")
            .task { syncFromProfile() }
            .alert("Couldn't save", isPresented: .constant(error != nil)) {
                Button("OK") { error = nil }
            } message: {
                Text(error?.userMessage ?? "")
            }
        }
    }

    private var accountSection: some View {
        Section("Account") {
            if let me {
                LabeledContent("Signed in as", value: me.displayName)
                if let email = me.email, !email.isEmpty {
                    LabeledContent("Email", value: email)
                }
            }
            Button("Sign out") {
                Task {
                    // Deregister first: the session is what authorises the
                    // call, so doing it after sign-out would 401 and leave
                    // the device receiving pushes for an account nobody is
                    // signed into.
                    await push.deregister()
                    await auth.signOut()
                }
            }
        }
    }

    /// Appearance. `System` is the default and defers to the device, which is
    /// what the app did before this control existed -- so a user who never
    /// opens this section sees no change.
    private var appearanceSection: some View {
        @Bindable var theme = theme
        return Section("Appearance") {
            Picker("Theme", selection: $theme.choice) {
                ForEach(ThemeChoice.allCases) { choice in
                    Text(choice.label).tag(choice)
                }
            }
            .pickerStyle(.segmented)
        }
    }

    /// Whether a toggle's new value came from a tap rather than from
    /// `syncFromProfile`.
    ///
    /// `.onChange` cannot tell the two apart — a programmatic write fires it
    /// exactly like a user's does — so opening this screen used to ask the OS
    /// for notification authorization and PUT all three preferences back,
    /// every time. A plain `isSyncing` flag does not close it: SwiftUI
    /// delivers the change during the *next* update pass, by which point a
    /// flag set and cleared inside the sync has already gone false. Comparing
    /// against the profile does, and needs no state: a sync writes the value
    /// the profile already holds, and a tap never does.
    private func isUserEdit(_ newValue: Bool, matching current: Bool?) -> Bool {
        newValue != (current ?? false)
    }

    private var notificationsSection: some View {
        Section {
            Toggle("Push notifications", isOn: $pushOptIn)
                .onChange(of: pushOptIn) { _, newValue in
                    guard isUserEdit(newValue, matching: me?.pushOptIn) else { return }
                    Task { await setPush(newValue) }
                }
            Toggle("Daily email digest", isOn: $digestOptIn)
                .onChange(of: digestOptIn) { _, newValue in
                    guard isUserEdit(newValue, matching: me?.digestOptIn) else { return }
                    Task { await save(digest: newValue) }
                }
                .disabled(me?.hasEmail != true)
            Toggle("Email me new shows immediately", isOn: $instantOptIn)
                .onChange(of: instantOptIn) { _, newValue in
                    guard isUserEdit(newValue, matching: me?.instantNotifyOptIn) else { return }
                    Task { await save(instantNotify: newValue) }
                }
                .disabled(me?.hasEmail != true)
        } header: {
            Text("Notifications")
        } footer: {
            if me?.hasEmail != true {
                Text("Sign in again to grant email access before turning on email notifications.")
            } else {
                Text("We only notify you about artists you follow here.")
            }
        }
    }

    private var aboutSection: some View {
        Section("About") {
            // Native pages rather than a web view: better for App Review,
            // and they work with no network.
            NavigationLink("Privacy Policy") { PolicyView(kind: .privacy, api: api) }
            NavigationLink("Terms of Service") { PolicyView(kind: .terms, api: api) }
            LabeledContent("Version", value: Self.versionString)
            SpotifyAttribution()
        }
    }

    /// Disconnecting Spotify without deleting the account.
    ///
    /// Its own section above the danger zone, and deliberately not styled
    /// destructive: this is the recoverable option -- saves, subscriptions and
    /// preferences survive, and signing in again restores the account. Putting
    /// it beside "Delete account" in red would push people toward the
    /// irreversible one.
    ///
    /// Guideline 5.1.1(v) asks for a way to revoke the connection from inside
    /// the app. Account deletion arguably answered that, but it was the only
    /// answer (plan §10.1.2).
    private var connectionSection: some View {
        Section {
            Button {
                showingDisconnectConfirmation = true
            } label: {
                if isDisconnecting {
                    HStack { ProgressView(); Text("Disconnecting…") }
                } else {
                    Text("Disconnect Spotify")
                }
            }
            .disabled(isDisconnecting)
            .confirmationDialog(
                "Disconnect Spotify?",
                isPresented: $showingDisconnectConfirmation,
                titleVisibility: .visible
            ) {
                Button("Disconnect") {
                    Task { await disconnectSpotify() }
                }
                Button("Cancel", role: .cancel) {}
            } message: {
                Text("We'll delete the Spotify credential we hold and the listening profile built from it, and sign you out. Your saved shows and artist alerts are kept.")
            }
        } footer: {
            // Saying only "disconnected" would overclaim: deleting our copy
            // stops us using the grant, but the authorisation still exists on
            // Spotify's side until the user removes it there.
            Text("This stops ConcertFinder using your Spotify account. To also remove ConcertFinder from Spotify's connected apps, visit spotify.com/account/apps.")
        }
    }

    /// Account deletion has to be reachable in-app and must actually delete,
    /// not deactivate — App Store Guideline 5.1.1(v). The confirmation is
    /// deliberately a destructive-styled two-step, because it is irreversible
    /// and takes the Spotify refresh token and every device registration with
    /// it.
    private var dangerSection: some View {
        Section {
            Button(role: .destructive) {
                showingDeleteConfirmation = true
            } label: {
                if isDeleting {
                    HStack { ProgressView(); Text("Deleting…") }
                } else {
                    Text("Delete account")
                }
            }
            .disabled(isDeleting)
            .confirmationDialog(
                "Delete your account?",
                isPresented: $showingDeleteConfirmation,
                titleVisibility: .visible
            ) {
                Button("Delete permanently", role: .destructive) {
                    Task { await deleteAccount() }
                }
                Button("Cancel", role: .cancel) {}
            } message: {
                Text("This removes your profile, saved shows, artist alerts and your Spotify connection. It cannot be undone.")
            }
        } footer: {
            Text("Deleting removes everything immediately. Your listening history is never stored — only the artist scores we derive from it.")
        }
    }

    // MARK: - Actions

    private func syncFromProfile() {
        guard let me else { return }
        digestOptIn = me.digestOptIn ?? false
        instantOptIn = me.instantNotifyOptIn ?? false
        pushOptIn = me.pushOptIn ?? false
    }

    /// Turning push on has two halves: the server preference and the OS
    /// permission. Asking the OS first means a user who declines the system
    /// prompt does not end up with the toggle on and nothing arriving.
    private func setPush(_ enabled: Bool) async {
        if enabled {
            let granted = await push.requestAuthorizationAndRegister()
            guard granted else {
                pushOptIn = false
                error = .unknown("Notifications are off for ConcertFinder. You can turn them on in Settings.")
                return
            }
        } else {
            await push.deregister()
        }
        await save(push: enabled)
    }

    private func save(digest: Bool? = nil, instantNotify: Bool? = nil, push pushValue: Bool? = nil) async {
        do {
            try await api.updatePreferences(digest: digest, instantNotify: instantNotify, push: pushValue)
            if var updated = me {
                if let digest { updated.digestOptIn = digest }
                if let instantNotify { updated.instantNotifyOptIn = instantNotify }
                if let pushValue { updated.pushOptIn = pushValue }
                auth.updateProfile(updated)
            }
        } catch let apiError as APIError {
            error = apiError
            // Put the toggle back so it reflects what the server actually
            // holds rather than what the tap intended.
            syncFromProfile()
        } catch {
            self.error = .unknown(error.localizedDescription)
            syncFromProfile()
        }
    }

    private func deleteAccount() async {
        isDeleting = true
        defer { isDeleting = false }
        do {
            await push.deregister()
            // The display name is the server's confirmation token. Sending no
            // body at all is what this used to do, and the handler rejected
            // every one of them with 400.
            try await api.deleteAccount(confirmName: me?.displayName ?? "")
            await auth.signOut()
        } catch let apiError as APIError {
            error = apiError
        } catch {
            self.error = .unknown(error.localizedDescription)
        }
    }

    private func disconnectSpotify() async {
        isDisconnecting = true
        defer { isDisconnecting = false }
        do {
            // Deregister first, as with sign-out: the session authorises the
            // call, and the disconnect ends every session server-side.
            await push.deregister()
            try await api.disconnectSpotify()
            await auth.signOut()
        } catch let apiError as APIError {
            error = apiError
        } catch {
            self.error = .unknown(error.localizedDescription)
        }
    }

    private static var versionString: String {
        let info = Bundle.main.infoDictionary
        let version = info?["CFBundleShortVersionString"] as? String ?? "1.0.0"
        let build = info?["CFBundleVersion"] as? String ?? "0"
        return "\(version) (\(build))"
    }
}

/// Privacy and terms, rendered natively.
///
/// The privacy story here is unusually good and worth stating plainly: raw
/// listening data is never persisted — it is held in memory and discarded
/// after the profile is built — and only the derived affinity scores are
/// stored, with a 24-hour TTL.
struct PolicyView: View {
    enum Kind {
        case privacy
        case terms

        var title: String {
            switch self {
            case .privacy: "Privacy Policy"
            case .terms: "Terms of Service"
            }
        }
    }

    let kind: Kind
    let api: APIClient

    @State private var info: SiteInfo?

    var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: Metrics.gutter) {
                if let effective = info?.effectiveDate {
                    Text("Effective \(effective)")
                        .font(.caption)
                        .foregroundStyle(.secondary)
                }
                ForEach(Array(sections.enumerated()), id: \.offset) { _, section in
                    VStack(alignment: .leading, spacing: 6) {
                        Text(section.heading).font(.headline)
                        Text(section.body).font(.subheadline)
                    }
                }
                if let contact = info?.contactEmail, !contact.isEmpty {
                    VStack(alignment: .leading, spacing: 6) {
                        Text("Contact").font(.headline)
                        Text(contact).font(.subheadline)
                    }
                }
            }
            .padding(Metrics.gutter)
            .frame(maxWidth: .infinity, alignment: .leading)
        }
        .navigationTitle(kind.title)
        .navigationBarTitleDisplayMode(.inline)
        .task { info = try? await api.siteInfo() }
    }

    private var sections: [(heading: String, body: String)] {
        switch kind {
        case .privacy:
            [
                ("What we store",
                 "Your Spotify user ID and display name, your email address if you granted it, the location you set, and a device token if you turned on push notifications."),
                ("What we never store",
                 "Your listening history. Saved tracks, top artists, recently played and playlists are read to build a profile, held in memory, and discarded. Only the derived artist scores are kept, and they expire after 24 hours."),
                ("Your Spotify connection",
                 "The app itself never receives a Spotify token. All Spotify access happens on our server, and the refresh token is encrypted at rest."),
                ("Deleting your data",
                 "Delete your account from Settings. It removes everything immediately, including your Spotify connection and any device tokens."),
            ]
        case .terms:
            [
                ("Use of the service",
                 "ConcertFinder is a personal-scale, non-commercial concert-discovery tool for personal use."),
                ("Spotify",
                 "You need a Spotify account to sign in, and you agree to Spotify's own terms. ConcertFinder is not affiliated with or endorsed by Spotify."),
                ("Concert data",
                 "Listings are aggregated from public third-party APIs and may be incomplete or out of date. Ticket purchases happen on those third-party sites, not here."),
                ("No warranty",
                 "The service is provided as-is. Always confirm details with the venue or ticket seller before travelling."),
            ]
        }
    }
}
