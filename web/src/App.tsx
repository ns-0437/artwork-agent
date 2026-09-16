import CaseView from './pages/CaseView'

export default function App() {
  return (
    <div className="app-shell">
      <header className="app-header">
        <span className="kicker">
          <span className="dot" />
          Independent prototype
        </span>
        <h1>Artwork Exception Resolution Agent</h1>
        <p>
          Inspects a sticker order's artwork, clarifies intent when needed, repairs one eligible defect, verifies
          it, and prepares a proof — with the case persisted so work survives a crash or a delayed reply. Not
          affiliated with, and has no access to, any real company's production systems.
        </p>
      </header>
      <CaseView />
      <footer className="app-footer">
        Artwork Exception Resolution Agent — a portfolio prototype. See the repository README for the evaluation
        results and architecture notes.
      </footer>
    </div>
  )
}
