import { ref, computed } from 'vue'
import { operationLogger } from '../services/operationLogger'
import { RetryManager } from '../services/retryManager'

export const ButtonState = {
  IDLE: 'idle',
  LOADING: 'loading',
  SUCCESS: 'success',
  ERROR: 'error',
  DISABLED: 'disabled'
}

export const useButtonControl = (options = {}) => {
  const {
    action = 'button',
    debounceDelay = 0,
    throttleDelay = 0,
    timeout = 30000,
    retry = false,
    retryOptions = {},
    onSuccess = null,
    onError = null
  } = options

  const state = ref(ButtonState.IDLE)
  const error = ref(null)
  const isLoading = computed(() => state.value === ButtonState.LOADING)
  const isDisabled = computed(() => [ButtonState.LOADING, ButtonState.DISABLED].includes(state.value))
  const canClick = computed(() => state.value === ButtonState.IDLE || state.value === ButtonState.ERROR)
  const lastResult = ref(null)
  const internalTimeoutId = ref(null)
  const lastClickTime = ref(0)
  const isThrottled = ref(false)

  const clearInternalTimeout = () => {
    if (internalTimeoutId.value) {
      window.clearTimeout(internalTimeoutId.value)
      internalTimeoutId.value = null
    }
  }

  const handleTimeout = () => {
    if (state.value === ButtonState.LOADING) {
      state.value = ButtonState.ERROR
      error.value = new Error('请求超时')
      operationLogger.error('Request timeout', { action })
      if (onError) {
        onError(error.value, { type: 'timeout' })
      }
      setTimeout(() => {
        if (state.value === ButtonState.ERROR) {
          state.value = ButtonState.IDLE
        }
      }, 3000)
    }
  }

  const reset = () => {
    state.value = ButtonState.IDLE
    error.value = null
    lastResult.value = null
    clearInternalTimeout()
  }

  const setLoading = (loading) => {
    state.value = loading ? ButtonState.LOADING : ButtonState.IDLE
  }

  const execute = async (fn, execOptions = {}) => {
    const {
      disableTimeout = false,
      disableRetry = false
    } = execOptions

    if (!canClick.value) {
      operationLogger.debug('Button click rejected: not clickable', { action, state: state.value })
      return lastResult.value
    }

    const now = Date.now()
    if (throttleDelay > 0 && now - lastClickTime.value < throttleDelay) {
      operationLogger.debug('Button click throttled', { action, wait: throttleDelay })
      isThrottled.value = true
      setTimeout(() => { isThrottled.value = false }, throttleDelay - (now - lastClickTime.value))
      return null
    }
    lastClickTime.value = now

    state.value = ButtonState.LOADING
    error.value = null
    operationLogger.info('Button action started', { action })

    if (!disableTimeout && timeout > 0) {
      clearInternalTimeout()
      internalTimeoutId.value = window.setTimeout(() => {
        handleTimeout()
      }, timeout)
    }

    try {
      let result
      if (retry && !disableRetry) {
        const manager = new RetryManager({
          ...retryOptions,
          onRetry: (info) => {
            operationLogger.info('Retrying action', { action, ...info })
          }
        })
        const retryResult = await manager.executeWithConfirm(fn)
        if (retryResult.success) {
          result = retryResult.data
        } else {
          throw retryResult.error
        }
      } else {
        result = await fn()
      }

      clearInternalTimeout()
      state.value = ButtonState.SUCCESS
      lastResult.value = result
      operationLogger.info('Button action succeeded', { action })

      if (onSuccess) {
        onSuccess(result)
      }

      setTimeout(() => {
        if (state.value === ButtonState.SUCCESS) {
          state.value = ButtonState.IDLE
        }
      }, 2000)

      return result
    } catch (e) {
      clearInternalTimeout()
      state.value = ButtonState.ERROR
      error.value = e
      operationLogger.error('Button action failed', { action, error: e.message })

      if (onError) {
        onError(e, { type: 'error' })
      }

      setTimeout(() => {
        if (state.value === ButtonState.ERROR) {
          state.value = ButtonState.IDLE
        }
      }, 3000)

      return null
    }
  }

  return {
    state,
    error,
    isLoading,
    isDisabled,
    canClick,
    isThrottled,
    lastResult,
    reset,
    setLoading,
    execute
  }
}

export const createButtonPool = (size = 10) => {
  const buttons = new Map()
  let counter = 0

  const acquire = (options) => {
    const id = ++counter
    const button = useButtonControl(options)
    buttons.set(id, button)
    return { id, ...button }
  }

  const release = (id) => {
    const button = buttons.get(id)
    if (button) {
      button.reset()
      buttons.delete(id)
    }
  }

  const clear = () => {
    buttons.forEach(button => button.reset())
    buttons.clear()
  }

  return {
    buttons,
    acquire,
    release,
    clear
  }
}

export default {
  useButtonControl,
  createButtonPool,
  ButtonState
}
