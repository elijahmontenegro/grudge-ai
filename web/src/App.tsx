import { Routes, Route } from 'react-router'
import { HomePage } from './pages/Home'
import { ThreadPage } from './pages/Thread'
import { SettingsPage } from './pages/Settings'

export default function App() {
  return (
    <Routes>
      <Route path="/" element={<HomePage />} />
      <Route path="/thread/:threadId" element={<ThreadPage />} />
      <Route path="/settings" element={<SettingsPage />} />
    </Routes>
  )
}
