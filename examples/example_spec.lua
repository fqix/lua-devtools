describe('Example (a+b)?', function()
    local value

    before_each(function()
        value = 41
        print('SETUP')
    end)

    after_each(function()
        print('TEARDOWN')
    end)

    context('nested', function()
        it('selected [1]', function()
            local actual = value + 1
            assert.are.equal(42, actual)
            print('SELECTED')
        end)

        it('other', function()
            print('OTHER')
        end)
    end)
end)

describe('Another suite', function()
    it('selected [1]', function()
        print('OTHER-SUITE')
    end)
end)
