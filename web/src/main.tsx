import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import '@fontsource/geist/400.css'
import '@fontsource/geist/500.css'
import '@fontsource/geist/600.css'
import '@fontsource/geist-mono/400.css'
import '@fontsource/geist-mono/500.css'
import '@fontsource/geist-mono/600.css'
import './index.css'
import App from './App.tsx'
import { DemoApp } from './demo/DemoApp.tsx'
import { ThemeProvider } from '@/components/ThemeProvider'

const RootApp = import.meta.env.VITE_DEMO_MODE === 'true' ? DemoApp : App

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <ThemeProvider>
      <RootApp />
    </ThemeProvider>
  </StrictMode>,
)
