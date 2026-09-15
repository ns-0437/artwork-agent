import CaseView from './pages/CaseView'

export default function App() {
  return (
    <div style={{ fontFamily: 'system-ui, sans-serif', maxWidth: 720, margin: '2rem auto', padding: '0 1rem' }}>
      <h1>Artwork Exception Resolution Agent</h1>
      <p style={{ color: '#666' }}>
        Independent portfolio prototype. Not affiliated with, and has no access to, any real company's
        production systems.
      </p>
      <CaseView />
    </div>
  )
}
