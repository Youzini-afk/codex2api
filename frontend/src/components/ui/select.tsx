import { Check, ChevronDown } from 'lucide-react'
import { useCallback, useEffect, useLayoutEffect, useRef, useState } from 'react'
import { createPortal } from 'react-dom'
import { cn } from '@/lib/utils'

export interface SelectOption {
  label: string
  value: string
}

interface SelectProps {
  value: string
  onValueChange: (value: string) => void
  options: SelectOption[]
  placeholder?: string
  disabled?: boolean
  className?: string
  compact?: boolean
}

interface DropdownPosition {
  top: number
  left: number
  width: number
  maxHeight: number
  openUp: boolean
}

const DROPDOWN_GAP = 8
const DROPDOWN_MAX_HEIGHT = 288
const VIEWPORT_PADDING = 8

export function Select({
  value,
  onValueChange,
  options,
  placeholder = '请选择',
  disabled = false,
  className,
  compact = false,
}: SelectProps) {
  const [open, setOpen] = useState(false)
  const [position, setPosition] = useState<DropdownPosition | null>(null)
  const triggerRef = useRef<HTMLButtonElement>(null)
  const dropdownRef = useRef<HTMLDivElement>(null)
  const selectedOption = options.find((option) => option.value === value)

  const computePosition = useCallback(() => {
    const trigger = triggerRef.current
    if (!trigger) return
    const rect = trigger.getBoundingClientRect()
    const viewportHeight = window.innerHeight
    const spaceBelow = viewportHeight - rect.bottom - DROPDOWN_GAP - VIEWPORT_PADDING
    const spaceAbove = rect.top - DROPDOWN_GAP - VIEWPORT_PADDING
    const openUp = spaceBelow < Math.min(DROPDOWN_MAX_HEIGHT, 160) && spaceAbove > spaceBelow
    const maxHeight = Math.max(120, Math.min(DROPDOWN_MAX_HEIGHT, openUp ? spaceAbove : spaceBelow))
    setPosition({
      top: openUp ? rect.top - DROPDOWN_GAP : rect.bottom + DROPDOWN_GAP,
      left: rect.left,
      width: rect.width,
      maxHeight,
      openUp,
    })
  }, [])

  useLayoutEffect(() => {
    if (!open) return
    computePosition()
  }, [open, computePosition, options.length])

  useEffect(() => {
    if (!open) return

    const handlePointerDown = (event: MouseEvent) => {
      const target = event.target as Node
      if (triggerRef.current?.contains(target)) return
      if (dropdownRef.current?.contains(target)) return
      setOpen(false)
    }

    const handleEscape = (event: KeyboardEvent) => {
      if (event.key === 'Escape') {
        setOpen(false)
      }
    }

    const handleReposition = () => computePosition()

    document.addEventListener('mousedown', handlePointerDown)
    document.addEventListener('keydown', handleEscape)
    window.addEventListener('resize', handleReposition)
    window.addEventListener('scroll', handleReposition, true)

    return () => {
      document.removeEventListener('mousedown', handlePointerDown)
      document.removeEventListener('keydown', handleEscape)
      window.removeEventListener('resize', handleReposition)
      window.removeEventListener('scroll', handleReposition, true)
    }
  }, [open, computePosition])

  return (
    <div className={cn('relative w-full', className)}>
      <button
        ref={triggerRef}
        type="button"
        disabled={disabled}
        aria-haspopup="listbox"
        aria-expanded={open}
        className={cn(
          'flex w-full items-center justify-between gap-3 border border-input bg-background text-left shadow-xs transition-[border-color,box-shadow,transform] outline-none',
          compact ? 'h-8 rounded-lg px-2.5 text-[13px]' : 'h-11 rounded-xl px-3.5 text-[15px]',
          'hover:border-primary/30 hover:bg-accent/50',
          'focus-visible:border-ring focus-visible:ring-[3px] focus-visible:ring-ring/20',
          'disabled:cursor-not-allowed disabled:opacity-60',
          open && 'border-primary/35 ring-[3px] ring-primary/10'
        )}
        onClick={() => {
          if (!disabled) {
            setOpen((current) => !current)
          }
        }}
      >
        <span className={cn('truncate', selectedOption ? 'text-foreground' : 'text-muted-foreground')}>
          {selectedOption?.label ?? placeholder}
        </span>
        <ChevronDown className={cn('size-4 shrink-0 text-muted-foreground transition-transform', open && 'rotate-180')} />
      </button>

      {open && position
        ? createPortal(
            <div
              ref={dropdownRef}
              style={{
                position: 'fixed',
                top: position.openUp ? undefined : position.top,
                bottom: position.openUp ? window.innerHeight - position.top : undefined,
                left: position.left,
                width: position.width,
              }}
              className={cn(
                'z-[1000] overflow-hidden border border-border bg-popover shadow-[0_18px_40px_hsl(222_30%_18%/0.12)] backdrop-blur-sm rounded-lg'
              )}
            >
              <div
                className={cn('overflow-auto', compact ? 'p-1' : 'p-2')}
                style={{ maxHeight: position.maxHeight }}
              >
                <div role="listbox" aria-activedescendant={value || undefined} className={compact ? 'space-y-0.5' : 'space-y-1'}>
                  {options.map((option) => {
                    const isSelected = option.value === value
                    return (
                      <button
                        key={option.value}
                        id={option.value}
                        type="button"
                        role="option"
                        aria-selected={isSelected}
                        className={cn(
                          'flex w-full items-center justify-between gap-3 text-left transition-colors',
                          compact ? 'rounded-md px-2 py-1.5 text-[13px]' : 'rounded-md px-3 py-2.5 text-[15px]',
                          isSelected
                            ? 'bg-primary/10 text-primary'
                            : 'text-foreground hover:bg-accent/70 hover:text-accent-foreground'
                        )}
                        onClick={() => {
                          onValueChange(option.value)
                          setOpen(false)
                        }}
                      >
                        <span className="truncate">{option.label}</span>
                        <Check className={cn('size-4 shrink-0', isSelected ? 'opacity-100' : 'opacity-0')} />
                      </button>
                    )
                  })}
                </div>
              </div>
            </div>,
            document.body
          )
        : null}
    </div>
  )
}
