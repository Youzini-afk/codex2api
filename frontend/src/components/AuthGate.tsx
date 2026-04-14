import type { PropsWithChildren } from 'react'
import { createContext, useCallback, useContext, useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { ADMIN_AUTH_REQUIRED_EVENT, api, clearAdminKey, resetAdminAuthState } from '../api'
import logoImg from '../assets/logo.png'
import { getErrorMessage } from '../utils/error'

type AuthStatus = 'checking' | 'authenticated' | 'need_login'

type AdminAuthContextValue = {
  authRequired: boolean
  authenticated: boolean
  logout: () => Promise<void>
}

const AdminAuthContext = createContext<AdminAuthContextValue>({
  authRequired: false,
  authenticated: true,
  logout: async () => {},
})

export function useAdminAuth() {
  return useContext(AdminAuthContext)
}

export default function AuthGate({ children }: PropsWithChildren) {
  const { t } = useTranslation()
  const [status, setStatus] = useState<AuthStatus>('checking')
  const [authRequired, setAuthRequired] = useState(false)
  const [inputKey, setInputKey] = useState('')
  const [error, setError] = useState('')
  const [submitting, setSubmitting] = useState(false)

  const checkAuth = useCallback(async () => {
    try {
      const session = await api.getAdminSessionStatus()
      setAuthRequired(session.auth_required)
      setStatus(session.authenticated ? 'authenticated' : 'need_login')
      if (!session.auth_required || session.auth_method === 'session') {
        clearAdminKey()
      }
    } catch {
      setStatus('authenticated')
    }
  }, [])

  useEffect(() => {
    void checkAuth()
  }, [checkAuth])

  useEffect(() => {
    const timer = window.setInterval(() => {
      void checkAuth()
    }, 30000)

    const handleAuthRequired = () => {
      setError('')
      setInputKey('')
      setStatus('checking')
      void checkAuth()
    }

    const handleStorage = (event: StorageEvent) => {
      if (event.key === 'admin_auth_reset_at') {
        handleAuthRequired()
      }
    }

    window.addEventListener(ADMIN_AUTH_REQUIRED_EVENT, handleAuthRequired)
    window.addEventListener('storage', handleStorage)
    return () => {
      window.clearInterval(timer)
      window.removeEventListener(ADMIN_AUTH_REQUIRED_EVENT, handleAuthRequired)
      window.removeEventListener('storage', handleStorage)
    }
  }, [checkAuth])

  const handleLogin = async () => {
    if (!inputKey.trim()) {
      setError(t('auth.error'))
      return
    }
    setSubmitting(true)
    setError('')
    try {
      const session = await api.loginAdminSession(inputKey.trim())
      clearAdminKey()
      setInputKey('')
      setAuthRequired(session.auth_required)
      setStatus(session.authenticated ? 'authenticated' : 'need_login')
      if (!session.authenticated) {
        setError(t('auth.error'))
      }
    } catch (loginError) {
      setError(getErrorMessage(loginError) || t('auth.error'))
    } finally {
      setSubmitting(false)
    }
  }

  const logout = useCallback(async () => {
    try {
      await api.logoutAdminSession()
    } catch {
      // ignore logout failure and reset local auth state anyway
    } finally {
      clearAdminKey()
      resetAdminAuthState()
    }
  }, [])

  if (status === 'checking') {
    return (
      <div className="flex items-center justify-center min-h-dvh">
        <div className="text-center">
          <div className="size-8 mx-auto mb-3 rounded-full border-3 border-primary/30 border-t-primary animate-spin" />
          <p className="text-sm text-muted-foreground">{t('common.loading')}</p>
        </div>
      </div>
    )
  }

  if (status === 'need_login') {
    return (
      <div className="flex items-center justify-center min-h-dvh bg-gradient-to-br from-slate-50 via-white to-blue-50/30">
        <div className="w-full max-w-[400px] mx-4">
          <div className="text-center mb-8">
            <img src={logoImg} alt="Codex2API" className="w-16 h-16 rounded-2xl object-cover shadow-[0_4px_20px_hsl(258_60%_63%/0.2)] mx-auto mb-4" />
            <h1 className="text-[28px] font-bold bg-gradient-to-br from-[hsl(258,60%,63%)] to-[hsl(210,80%,60%)] bg-clip-text text-transparent">
              Codex2API
            </h1>
            <p className="text-sm text-muted-foreground mt-1">{t('auth.subtitle')}</p>
          </div>

          <div className="rounded-3xl border border-border bg-white/80 shadow-xl shadow-black/[0.03] p-6 backdrop-blur-sm">
            <div className="space-y-4">
              <div>
                <label className="block mb-2 text-sm font-semibold text-muted-foreground">{t('settings.adminSecret')}</label>
                <input
                  type="password"
                  value={inputKey}
                  onChange={(e) => { setInputKey(e.target.value); setError('') }}
                  onKeyDown={(e) => { if (e.key === 'Enter') void handleLogin() }}
                  placeholder={t('auth.placeholder')}
                  autoFocus
                  className="w-full h-11 px-4 rounded-xl border border-border bg-white text-[15px] outline-none transition-all focus:border-primary/40 focus:ring-2 focus:ring-primary/10"
                />
              </div>

              {error && (
                <div className="text-sm text-red-500 font-medium px-1">{error}</div>
              )}

              <button
                onClick={() => void handleLogin()}
                disabled={submitting}
                className="w-full h-11 rounded-xl bg-gradient-to-r from-[hsl(258,60%,63%)] to-[hsl(210,80%,60%)] text-white font-semibold text-[15px] shadow-lg shadow-primary/20 transition-all hover:opacity-90 disabled:opacity-50"
              >
                {submitting ? t('common.loading') : t('auth.login')}
              </button>
            </div>
          </div>
        </div>
      </div>
    )
  }

  return (
    <AdminAuthContext.Provider value={{ authRequired, authenticated: status === 'authenticated', logout }}>
      {children}
    </AdminAuthContext.Provider>
  )
}
